package server

import (
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"easylab/utils"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// kubeconfigFor is testKubeconfig with a caller-chosen API server, so a test can
// put two labs on genuinely different clusters.
func kubeconfigFor(server string) string {
	return strings.Replace(testKubeconfig("tok"), "https://cluster.example.com:6443", server, 1)
}

func TestClusterKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cfg  *LabConfig
		want string
	}{
		{name: "nil config shares nothing", cfg: nil, want: ""},
		{
			name: "dedicated cluster shares nothing",
			cfg:  &LabConfig{UseExistingCluster: false, ExternalKubeconfig: kubeconfigFor("https://a.example:6443")},
			want: "",
		},
		{
			name: "byo cluster keys on the api server",
			cfg:  &LabConfig{UseExistingCluster: true, ExternalKubeconfig: kubeconfigFor("https://a.example:6443")},
			want: "host:https://a.example:6443",
		},
		{
			name: "profile name is the fallback identity",
			cfg:  &LabConfig{UseExistingCluster: true, ClusterProfile: "workshop-cluster"},
			want: "profile:workshop-cluster",
		},
		{
			name: "kubeconfig wins over the profile name",
			cfg: &LabConfig{
				UseExistingCluster: true,
				ExternalKubeconfig: kubeconfigFor("https://a.example:6443"),
				ClusterProfile:     "workshop-cluster",
			},
			want: "host:https://a.example:6443",
		},
		{
			name: "unparseable kubeconfig falls back to the profile",
			cfg:  &LabConfig{UseExistingCluster: true, ExternalKubeconfig: "not a kubeconfig", ClusterProfile: "p"},
			want: "profile:p",
		},
		{
			name: "byo cluster with no identity at all",
			cfg:  &LabConfig{UseExistingCluster: true},
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, clusterKey(tt.cfg))
		})
	}
}

func TestClusterKeyDistinguishesClusters(t *testing.T) {
	t.Parallel()

	one := clusterKey(&LabConfig{UseExistingCluster: true, ExternalKubeconfig: kubeconfigFor("https://a.example:6443")})
	two := clusterKey(&LabConfig{UseExistingCluster: true, ExternalKubeconfig: kubeconfigFor("https://b.example:6443")})
	require.NotEmpty(t, one)
	assert.NotEqual(t, one, two)
}

func TestClusterNeighbours(t *testing.T) {
	t.Parallel()

	const key = "profile:shared"
	jobOn := func(id string, status JobStatus, profile, namespace string) *Job {
		return &Job{
			ID:     id,
			Status: status,
			Config: &LabConfig{
				StackName:          id,
				UseExistingCluster: true,
				ClusterProfile:     profile,
				WorkspaceNamespace: namespace,
			},
		}
	}

	tests := []struct {
		name     string
		jobs     []*Job
		wantNS   []string
		excluded string
	}{
		{
			name:     "excludes the lab being created",
			jobs:     []*Job{jobOn("self", JobStatusPending, "shared", "workshops")},
			excluded: "self",
			wantNS:   nil,
		},
		{
			name:   "includes a running lab on the same cluster",
			jobs:   []*Job{jobOn("other", JobStatusRunning, "shared", "workshops")},
			wantNS: []string{"workshops"},
		},
		{
			name:   "excludes a lab on another cluster",
			jobs:   []*Job{jobOn("other", JobStatusCompleted, "elsewhere", "workshops")},
			wantNS: nil,
		},
		{
			name:   "excludes a destroyed lab",
			jobs:   []*Job{jobOn("other", JobStatusDestroyed, "shared", "workshops")},
			wantNS: nil,
		},
		{
			name:   "excludes a dry run",
			jobs:   []*Job{jobOn("other", JobStatusDryRunCompleted, "shared", "workshops")},
			wantNS: nil,
		},
		{
			name:   "a failed lab still counts as occupying",
			jobs:   []*Job{jobOn("other", JobStatusFailed, "shared", "workshops")},
			wantNS: []string{"workshops"},
		},
		{
			name:   "an unset namespace reads as the default",
			jobs:   []*Job{jobOn("other", JobStatusCompleted, "shared", "")},
			wantNS: []string{defaultWorkspaceNamespace},
		},
		{
			name:   "skips a job with no config",
			jobs:   []*Job{{ID: "broken", Status: JobStatusRunning}},
			wantNS: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := clusterNeighbours(tt.jobs, key, tt.excluded)
			require.Len(t, got, len(tt.wantNS))
			for i, ns := range tt.wantNS {
				assert.Equal(t, ns, got[i].namespace)
			}
		})
	}
}

func TestClusterNeighboursWithoutKey(t *testing.T) {
	t.Parallel()

	// A lab on its own cluster has no key, and must not be told it has company.
	jobs := []*Job{{ID: "other", Status: JobStatusRunning, Config: &LabConfig{WorkspaceNamespace: "workshops"}}}
	assert.Empty(t, clusterNeighbours(jobs, "", "self"))
}

// sittingLab is a neighbour that has already set up everything a joining lab can
// reuse: an ingress controller, cert-manager and the DNS-01 plumbing.
func sittingLab() clusterNeighbour {
	return clusterNeighbour{
		stackName:            "workshop-one",
		domain:               "one.example.com",
		namespace:            defaultWorkspaceNamespace,
		dnsProvider:          "ovh",
		dnsZone:              "example.com",
		clusterIssuerName:    "letsencrypt",
		certManagerNamespace: "cert-manager",
		ingressNamespace:     "traefik",
		ingressServiceName:   "traefik",
		ingressClassName:     "traefik",
		dnsCredentials:       map[string]string{"ovhAppKey": "shared-key"},
	}
}

// joiningLab is the second domain on the cluster, configured the way the wizard
// hands it over by default: install everything, DNS-01 not yet set up.
func joiningLab() *LabConfig {
	return &LabConfig{
		StackName:          "workshop-two",
		Domain:             "two.example.com",
		DNSProvider:        "ovh",
		DNSZone:            "example.com",
		UseExistingCluster: true,
		ClusterProfile:     "shared",
		DNSCredentials:     map[string]string{"ovhAppKey": "shared-key"},
	}
}

func TestAdoptSharedClusterMovesNamespaceAndReusesInfra(t *testing.T) {
	t.Parallel()

	cfg := joiningLab()
	notices := adoptSharedCluster(cfg, "job-11112222-3333-4444-5555-666677778888", []clusterNeighbour{sittingLab()})

	// The namespace is the change that makes a second domain possible at all.
	assert.NotEqual(t, defaultWorkspaceNamespace, cfg.WorkspaceNamespace)
	assert.Equal(t, "workshops-workshop-two", cfg.WorkspaceNamespace)

	// Reinstalling either Helm release would fail on its name.
	require.NotNil(t, cfg.InstallNginxIngress)
	assert.False(t, *cfg.InstallNginxIngress)
	require.NotNil(t, cfg.InstallCertManager)
	assert.False(t, *cfg.InstallCertManager)
	assert.Equal(t, "traefik", cfg.NginxIngressNamespace)
	assert.Equal(t, "traefik", cfg.NginxIngressServiceName)
	assert.Equal(t, "traefik", cfg.IngressClassName)
	assert.Equal(t, "cert-manager", cfg.CertManagerNamespace)

	// Recreating the DNS-01 plumbing is the reported apply conflict.
	assert.True(t, cfg.DNSAlreadyConfigured)
	assert.Equal(t, "letsencrypt", cfg.ClusterIssuerName)

	require.NotEmpty(t, notices)
	assert.Contains(t, strings.Join(notices, "\n"), "workshops-workshop-two")
}

func TestAdoptSharedClusterNoOp(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		cfg        *LabConfig
		neighbours []clusterNeighbour
	}{
		{name: "nil config", cfg: nil, neighbours: []clusterNeighbour{sittingLab()}},
		{name: "first lab on the cluster", cfg: joiningLab(), neighbours: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Empty(t, adoptSharedCluster(tt.cfg, "job-abc", tt.neighbours))
			if tt.cfg != nil {
				assert.Empty(t, tt.cfg.WorkspaceNamespace)
				assert.Nil(t, tt.cfg.InstallCertManager)
				assert.False(t, tt.cfg.DNSAlreadyConfigured)
			}
		})
	}
}

func TestAdoptSharedClusterQuickStartLabKeepsItsNamespaceOnly(t *testing.T) {
	t.Parallel()

	// No domain means no cert-manager and no certificate, so there is nothing to
	// reuse — but the namespace still has to be its own.
	cfg := &LabConfig{StackName: "quick", UseExistingCluster: true, ClusterProfile: "shared"}
	notices := adoptSharedCluster(cfg, "job-abc", []clusterNeighbour{sittingLab()})

	assert.Equal(t, "workshops-quick", cfg.WorkspaceNamespace)
	assert.Nil(t, cfg.InstallCertManager)
	assert.False(t, cfg.DNSAlreadyConfigured)
	require.Len(t, notices, 1)
}

func TestAdoptSharedClusterRespectsExplicitChoices(t *testing.T) {
	t.Parallel()

	cfg := joiningLab()
	cfg.WorkspaceNamespace = "team-two"
	cfg.InstallNginxIngress = boolPtr(false)
	cfg.NginxIngressNamespace = "ingress-nginx"
	cfg.ClusterIssuerName = "my-issuer"

	notices := adoptSharedCluster(cfg, "job-abc", []clusterNeighbour{sittingLab()})

	// A namespace nobody holds is left exactly as the admin set it.
	assert.Equal(t, "team-two", cfg.WorkspaceNamespace)
	// An admin who already pointed at their own controller is not overridden.
	assert.Equal(t, "ingress-nginx", cfg.NginxIngressNamespace)
	assert.Equal(t, "my-issuer", cfg.ClusterIssuerName)
	assert.NotContains(t, strings.Join(notices, "\n"), "ingress controller already on this cluster")
}

func TestAdoptSharedClusterWithoutADNS01Host(t *testing.T) {
	t.Parallel()

	// The sitting lab runs on nip.io: it installed no cert-manager and set up no
	// DNS-01, so there is nothing for the joining lab to reuse.
	neighbour := sittingLab()
	neighbour.domain = ""
	neighbour.dnsProvider = ""

	cfg := joiningLab()
	adoptSharedCluster(cfg, "job-abc", []clusterNeighbour{neighbour})

	assert.Equal(t, "workshops-workshop-two", cfg.WorkspaceNamespace)
	assert.Nil(t, cfg.InstallCertManager)
	assert.False(t, cfg.DNSAlreadyConfigured)
}

func TestAdoptSharedClusterFallsBackToTheDefaultIssuerInItsNotice(t *testing.T) {
	t.Parallel()

	neighbour := sittingLab()
	neighbour.clusterIssuerName = ""

	cfg := joiningLab()
	notices := adoptSharedCluster(cfg, "job-abc", []clusterNeighbour{neighbour})

	assert.True(t, cfg.DNSAlreadyConfigured)
	assert.Contains(t, strings.Join(notices, "\n"), utils.DefaultClusterIssuerName)
}

func TestAdoptSharedClusterHazards(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		mutate     func(*LabConfig, *clusterNeighbour)
		wantNotice string
		wantAbsent string
	}{
		{
			name:       "external dns on the joining lab",
			mutate:     func(cfg *LabConfig, _ *clusterNeighbour) { cfg.UseExternalDNS = true },
			wantNotice: "ExternalDNS cannot be shared",
		},
		{
			name:       "external dns on the sitting lab",
			mutate:     func(_ *LabConfig, n *clusterNeighbour) { n.usesExternalDNS = true },
			wantNotice: "ExternalDNS cannot be shared",
		},
		{
			name:       "neither lab uses external dns",
			mutate:     func(*LabConfig, *clusterNeighbour) {},
			wantAbsent: "ExternalDNS cannot be shared",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := joiningLab()
			neighbour := sittingLab()
			tt.mutate(cfg, &neighbour)

			joined := strings.Join(adoptSharedCluster(cfg, "job-abc", []clusterNeighbour{neighbour}), "\n")
			if tt.wantNotice != "" {
				assert.Contains(t, joined, tt.wantNotice)
			}
			if tt.wantAbsent != "" {
				assert.NotContains(t, joined, tt.wantAbsent)
			}
		})
	}
}

func TestFreeWorkspaceNamespace(t *testing.T) {
	t.Parallel()

	const jobID = "job-11112222-3333-4444-5555-666677778888"

	tests := []struct {
		name      string
		stackName string
		taken     map[string]bool
		want      string
	}{
		{
			name:      "prefers the readable stack-derived name",
			stackName: "devoxx",
			taken:     map[string]bool{"workshops": true},
			want:      "workshops-devoxx",
		},
		{
			name:      "adds the job suffix when the stack name is taken too",
			stackName: "devoxx",
			taken:     map[string]bool{"workshops": true, "workshops-devoxx": true},
			want:      "workshops-devoxx-11112222",
		},
		{
			name:      "falls back to the job suffix alone without a usable stack name",
			stackName: "!!!",
			taken:     map[string]bool{"workshops": true},
			want:      "workshops-11112222",
		},
		{
			name:      "sanitises an unruly stack name",
			stackName: "Devoxx BE.2026",
			taken:     map[string]bool{"workshops": true},
			want:      "workshops-devoxxbe-2026",
		},
		{
			name:      "reports exhaustion rather than returning a clash",
			stackName: "devoxx",
			taken: map[string]bool{
				"workshops":                 true,
				"workshops-devoxx":          true,
				"workshops-devoxx-11112222": true,
				"workshops-11112222":        true,
			},
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := freeWorkspaceNamespace(defaultWorkspaceNamespace, tt.stackName, jobID, tt.taken)
			assert.Equal(t, tt.want, got)
			if got != "" {
				assert.LessOrEqual(t, len(got), maxNamespaceLength)
				assert.False(t, tt.taken[got])
			}
		})
	}
}

func TestFreeWorkspaceNamespaceStaysWithinTheLabelLimit(t *testing.T) {
	t.Parallel()

	got := freeWorkspaceNamespace(defaultWorkspaceNamespace, strings.Repeat("a", 200), "job-1111", map[string]bool{"workshops": true})
	require.NotEmpty(t, got)
	assert.LessOrEqual(t, len(got), maxNamespaceLength)
	assert.False(t, strings.HasSuffix(got, "-"))
}

func TestResolveNamespaceClashReportsExhaustionWithoutChangingConfig(t *testing.T) {
	t.Parallel()

	cfg := &LabConfig{StackName: "devoxx", WorkspaceNamespace: "workshops"}
	neighbours := []clusterNeighbour{
		{stackName: "one", namespace: "workshops"},
		{stackName: "two", namespace: "workshops-devoxx"},
		{stackName: "three", namespace: "workshops-devoxx-11112222"},
		{stackName: "four", namespace: "workshops-11112222"},
	}

	notices := resolveNamespaceClash(cfg, "job-11112222-3333-4444-5555-666677778888", neighbours)

	assert.Equal(t, "workshops", cfg.WorkspaceNamespace, "a clash that cannot be resolved must not be silently kept")
	require.Len(t, notices, 1)
	assert.Contains(t, notices[0], "no free alternative")
}

func TestSameDNSCredentials(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		a, b map[string]string
		want bool
	}{
		{name: "both empty", want: true},
		{name: "equal", a: map[string]string{"k": "v"}, b: map[string]string{"k": "v"}, want: true},
		{name: "different value", a: map[string]string{"k": "v"}, b: map[string]string{"k": "w"}, want: false},
		{name: "different key", a: map[string]string{"k": "v"}, b: map[string]string{"j": "v"}, want: false},
		{name: "different size", a: map[string]string{"k": "v"}, b: map[string]string{"k": "v", "j": "w"}, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, sameDNSCredentials(tt.a, tt.b))
		})
	}
}

func TestSharedInfraURNsProtectsASharedWorkspaceNamespace(t *testing.T) {
	t.Parallel()

	const namespaceURN = "urn:pulumi:dev::easylab::kubernetes:core/v1:Namespace::workspace-namespace"

	shared := sharedInfraURNs("dev", destroyExclusions{useExistingCluster: true, sharedWorkspaceNamespace: true})
	assert.Contains(t, shared, namespaceURN,
		"a namespace another lab is in must survive this lab's destroy")

	alone := sharedInfraURNs("dev", destroyExclusions{useExistingCluster: true})
	assert.NotContains(t, alone, namespaceURN,
		"a lab alone in its namespace has to take it with it, or every destroy leaks a namespace")

	// The dedicated-cluster short circuit must stay absolute: excluding anything
	// there orphans the whole cloud cluster.
	assert.Empty(t, sharedInfraURNs("dev", destroyExclusions{sharedWorkspaceNamespace: true, ownsDNSIssuer: true}))
}

func TestSharedInfraURNsReleasesAPerLabIssuer(t *testing.T) {
	t.Parallel()

	const (
		issuerURN  = "urn:pulumi:dev::easylab::kubernetes:cert-manager.io/v1:ClusterIssuer::letsencrypt-prod-issuer"
		secretURN  = "urn:pulumi:dev::easylab::kubernetes:core/v1:Secret::dns-credentials-secret"
		webhookURN = "urn:pulumi:dev::easylab::kubernetes:helm.sh/v3:Release::cert-manager-webhook-ovh"
	)

	// A lab on the cluster-wide names must leave them for the other labs.
	onSharedNames := sharedInfraURNs("dev", destroyExclusions{useExistingCluster: true})
	assert.Contains(t, onSharedNames, issuerURN)
	assert.Contains(t, onSharedNames, secretURN)

	// A lab with its own issuer owns both outright and has to remove them, or it
	// leaves a dead issuer and a live copy of its DNS credentials on the cluster.
	ownIssuer := sharedInfraURNs("dev", destroyExclusions{useExistingCluster: true, ownsDNSIssuer: true})
	assert.NotContains(t, ownIssuer, issuerURN)
	assert.NotContains(t, ownIssuer, secretURN)

	// The webhook is cluster-wide either way.
	assert.Contains(t, ownIssuer, webhookURN)
}

func TestAdoptSharedClusterGivesADifferentlyCredentialedLabItsOwnIssuer(t *testing.T) {
	t.Parallel()

	cfg := joiningLab()
	cfg.DNSZone = "two.example"
	cfg.DNSCredentials = map[string]string{"ovhAppKey": "key-for-two"}

	notices := adoptSharedCluster(cfg, "job-11112222-3333-4444-5555-666677778888", []clusterNeighbour{sittingLab()})

	// Reusing the sitting issuer would issue with credentials that cannot write
	// this lab's zone, and fail silently.
	assert.False(t, cfg.DNSAlreadyConfigured)
	assert.True(t, cfg.DNSOwnIssuer)
	assert.Equal(t, "easylab-workshop-two-11112222", cfg.ClusterIssuerName)
	assert.Equal(t, "dns-credentials-ovh-workshop-two-11112222", cfg.DNSCredentialSecretName)

	// cert-manager and the ingress controller are still shared.
	require.NotNil(t, cfg.InstallCertManager)
	assert.False(t, *cfg.InstallCertManager)

	joined := strings.Join(notices, "\n")
	assert.Contains(t, joined, "its own ClusterIssuer")
	assert.Contains(t, joined, "two.example")
}

func TestAdoptSharedClusterKeepsAnExplicitIssuerName(t *testing.T) {
	t.Parallel()

	cfg := joiningLab()
	cfg.DNSCredentials = map[string]string{"ovhAppKey": "different"}
	cfg.ClusterIssuerName = "my-own-issuer"
	cfg.DNSCredentialSecretName = "my-own-secret"

	adoptSharedCluster(cfg, "job-abc", []clusterNeighbour{sittingLab()})

	assert.True(t, cfg.DNSOwnIssuer)
	assert.Equal(t, "my-own-issuer", cfg.ClusterIssuerName)
	assert.Equal(t, "my-own-secret", cfg.DNSCredentialSecretName)
}

func TestAdoptSharedClusterLeavesAnAlreadyDecidedIssuerAlone(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*LabConfig)
	}{
		{name: "already configured", mutate: func(c *LabConfig) { c.DNSAlreadyConfigured = true }},
		{name: "own issuer", mutate: func(c *LabConfig) { c.DNSOwnIssuer = true }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := joiningLab()
			tt.mutate(cfg)
			before := *cfg

			adoptSharedCluster(cfg, "job-abc", []clusterNeighbour{sittingLab()})

			assert.Equal(t, before.DNSAlreadyConfigured, cfg.DNSAlreadyConfigured)
			assert.Equal(t, before.DNSOwnIssuer, cfg.DNSOwnIssuer)
			assert.Empty(t, cfg.DNSCredentialSecretName)
		})
	}
}

func TestLabObjectSuffix(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		stackName string
		jobID     string
		want      string
	}{
		{name: "stack and job", stackName: "devoxx", jobID: "job-aabbccdd-1111", want: "devoxx-aabbccdd"},
		{name: "unusable stack name", stackName: "!!!", jobID: "job-aabbccdd-1111", want: "aabbccdd"},
		{name: "no job id", stackName: "devoxx", jobID: "", want: "devoxx"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, labObjectSuffix(tt.stackName, tt.jobID))
		})
	}
}

// A lab must not be able to claim an issuer that belongs to the whole cluster:
// it destroys what it owns, so "owning" the shared issuer would take certificate
// issuance down for every other lab on that cluster.
func TestAdoptSharedClusterRefusesToClaimASharedIssuerName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		issuer     string
		secret     string
		wantIssuer string
		wantSecret string
	}{
		{
			name:       "the project default issuer is not this lab's to own",
			issuer:     utils.DefaultClusterIssuerName,
			wantIssuer: "easylab-workshop-two-11112222",
			wantSecret: "dns-credentials-ovh-workshop-two-11112222",
		},
		{
			name:       "the sitting lab's issuer is not this lab's to own",
			issuer:     "letsencrypt", // sittingLab's issuer
			wantIssuer: "easylab-workshop-two-11112222",
			wantSecret: "dns-credentials-ovh-workshop-two-11112222",
		},
		{
			name:       "the provider-wide secret is not this lab's to own",
			secret:     "dns-credentials-ovh",
			wantIssuer: "easylab-workshop-two-11112222",
			wantSecret: "dns-credentials-ovh-workshop-two-11112222",
		},
		{
			name:       "a genuinely distinct name is kept",
			issuer:     "my-own-issuer",
			secret:     "my-own-secret",
			wantIssuer: "my-own-issuer",
			wantSecret: "my-own-secret",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := joiningLab()
			cfg.DNSCredentials = map[string]string{"ovhAppKey": "different"}
			cfg.ClusterIssuerName = tt.issuer
			cfg.DNSCredentialSecretName = tt.secret

			adoptSharedCluster(cfg, "job-11112222-3333-4444-5555-666677778888", []clusterNeighbour{sittingLab()})

			assert.True(t, cfg.DNSOwnIssuer)
			assert.Equal(t, tt.wantIssuer, cfg.ClusterIssuerName)
			assert.Equal(t, tt.wantSecret, cfg.DNSCredentialSecretName)

			// Whatever it ended up named, the destroy side must agree it is owned.
			pe := &PulumiExecutor{jobManager: &JobManager{jobs: ownIssuerJob(cfg)}}
			assert.True(t, pe.ownsDNSIssuer("self"),
				"a lab that adoption gave its own issuer must be allowed to destroy it")
		})
	}
}

// The per-lab issuer and secret are decided by EasyLab at deploy time, so the
// edit form has no field to round-trip them. Losing them on Edit & Retry would
// point the lab back at the cluster-wide credential secret a sibling lab owns,
// reinstall the fixed-name webhook, and strand this lab's credentials on destroy.
func TestHandler_RetryJobWithConfig_KeepsPerLabIssuer(t *testing.T) {
	jm := NewJobManager("")
	id := jm.CreateJob(&LabConfig{
		StackName:               "two",
		UseExistingCluster:      true,
		ExternalKubeconfig:      "orig-kubeconfig",
		Domain:                  "two.example.com",
		DNSProvider:             "ovh",
		DNSZone:                 "example.com",
		DNSCredentials:          map[string]string{"ovhAppKey": "key-for-two"},
		DNSOwnIssuer:            true,
		ClusterIssuerName:       "easylab-two-11112222",
		DNSCredentialSecretName: "dns-credentials-ovh-two-11112222",
	})
	require.NoError(t, jm.UpdateJobStatus(id, JobStatusFailed))

	h := NewHandler(jm, &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	w := httptest.NewRecorder()
	h.RetryJobWithConfig(w, retryWithConfigRequest(id, url.Values{
		"use_existing_cluster": {"true"},
		"domain":               {"two.example.com"},
		"dns_provider":         {"ovh"},
		"dns_zone":             {"example.com"},
		"cluster_issuer_name":  {"easylab-two-11112222"},
	}))
	assert.NotContains(t, w.Body.String(), "error-message")

	job, ok := jm.GetJob(id)
	require.True(t, ok)
	job.mu.RLock()
	defer job.mu.RUnlock()

	assert.True(t, job.Config.DNSOwnIssuer, "the lab must still own its issuer after a retry")
	assert.Equal(t, "dns-credentials-ovh-two-11112222", job.Config.DNSCredentialSecretName)

	// And the destroy side must still agree, or the retry has quietly made the
	// lab leave its DNS credentials on the cluster.
	pe := &PulumiExecutor{jobManager: jm}
	assert.True(t, pe.ownsDNSIssuer(id))
}

// "Already configured" is the admin asking to reuse the sitting lab's issuer.
// When their credentials cannot write this lab's zone that request cannot be
// honoured — the OVH webhook answers the challenge with the other account's keys
// and gets a 404 — so the credentials win and the override is reported.
func TestAdoptSharedClusterOverridesAlreadyConfiguredWhenCredentialsDiffer(t *testing.T) {
	t.Parallel()

	cfg := joiningLab()
	cfg.DNSAlreadyConfigured = true // as set by the wizard or Detect from cluster
	cfg.DNSCredentials = map[string]string{"ovhAppKey": "key-for-two"}

	notices := adoptSharedCluster(cfg, "job-11112222-3333-4444-5555-666677778888", []clusterNeighbour{sittingLab()})

	assert.False(t, cfg.DNSAlreadyConfigured,
		"ingress/https.go treats alreadyConfigured as the stronger signal, so it has to be cleared")
	assert.True(t, cfg.DNSOwnIssuer)
	assert.Equal(t, "easylab-workshop-two-11112222", cfg.ClusterIssuerName)
	assert.Equal(t, "dns-credentials-ovh-workshop-two-11112222", cfg.DNSCredentialSecretName)

	joined := strings.Join(notices, "\n")
	assert.Contains(t, joined, "overrides the \"Already configured\" setting")
	assert.Contains(t, joined, "404")
}

// A lab that entered no credentials has nothing to build an issuer from, so
// reuse is the only option — it must not get an empty per-lab secret.
func TestAdoptSharedClusterReusesWhenTheLabSuppliedNoCredentials(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		creds map[string]string
	}{
		{name: "no credentials at all", creds: nil},
		{name: "credentials present but blank", creds: map[string]string{"ovhAppKey": "", "ovhAppSecret": "  "}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := joiningLab()
			cfg.DNSCredentials = tt.creds

			notices := adoptSharedCluster(cfg, "job-abc", []clusterNeighbour{sittingLab()})

			assert.True(t, cfg.DNSAlreadyConfigured)
			assert.False(t, cfg.DNSOwnIssuer)
			assert.Empty(t, cfg.DNSCredentialSecretName)
			assert.Contains(t, strings.Join(notices, "\n"), "supplied no DNS credentials of its own")
		})
	}
}

func TestNormalizeAPIServer(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		host string
		want string
	}{
		{name: "lowercases the host", host: "https://API.Example.com:6443", want: "https://api.example.com:6443"},
		{name: "drops the default https port", host: "https://api.example.com:443", want: "https://api.example.com"},
		{name: "drops a trailing slash", host: "https://api.example.com:6443/", want: "https://api.example.com:6443"},
		{name: "leaves a non-default port alone", host: "https://api.example.com:6443", want: "https://api.example.com:6443"},
		{name: "handles an unparseable value", host: "not a url/", want: "not a url"},
		{name: "empty stays empty", host: "", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, normalizeAPIServer(tt.host))
		})
	}
}

func TestClusterKeyIgnoresAPIServerSpelling(t *testing.T) {
	t.Parallel()

	// Two kubeconfigs for one cluster must not read as two clusters: that would
	// leave both labs in the same namespace and let either destroy delete it.
	plain := clusterKey(&LabConfig{UseExistingCluster: true, ExternalKubeconfig: kubeconfigFor("https://api.example.com:443")})
	dressed := clusterKey(&LabConfig{UseExistingCluster: true, ExternalKubeconfig: kubeconfigFor("https://API.example.com/")})
	require.NotEmpty(t, plain)
	assert.Equal(t, plain, dressed)
}

func TestClusterNeighboursCopiesCredentials(t *testing.T) {
	t.Parallel()

	creds := map[string]string{"ovhAppKey": "original"}
	jobs := []*Job{{
		ID:     "other",
		Status: JobStatusCompleted,
		Config: &LabConfig{
			UseExistingCluster: true,
			ClusterProfile:     "shared",
			DNSCredentials:     creds,
		},
	}}

	got := clusterNeighbours(jobs, "profile:shared", "self")
	require.Len(t, got, 1)

	// The snapshot outlives the job's lock, so it must not alias live job state.
	creds["ovhAppKey"] = "mutated"
	assert.Equal(t, "original", got[0].dnsCredentials["ovhAppKey"])
}

// ownIssuerJob wraps one config as the single job "self" that ownsDNSIssuer looks up.
func ownIssuerJob(cfg *LabConfig) map[string]*Job {
	return map[string]*Job{"self": {ID: "self", Config: cfg}}
}

func TestOwnsDNSIssuer(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		jobs map[string]*Job
		want bool
	}{
		{
			name: "lab with its own issuer and its own secret",
			jobs: ownIssuerJob(&LabConfig{
				DNSOwnIssuer:            true,
				DNSProvider:             "ovh",
				ClusterIssuerName:       "easylab-two-11112222",
				DNSCredentialSecretName: "dns-credentials-ovh-two-11112222",
			}),
			want: true,
		},
		{
			name: "lab on the shared names",
			jobs: ownIssuerJob(&LabConfig{}),
			want: false,
		},
		// The flag alone must never release the shared objects: destroying the
		// cluster-wide issuer takes certificate issuance down for every other lab.
		{
			name: "flag set but the issuer is still the project default",
			jobs: ownIssuerJob(&LabConfig{
				DNSOwnIssuer:            true,
				DNSProvider:             "ovh",
				ClusterIssuerName:       utils.DefaultClusterIssuerName,
				DNSCredentialSecretName: "dns-credentials-ovh-two",
			}),
			want: false,
		},
		{
			name: "flag set but the issuer name is unset",
			jobs: ownIssuerJob(&LabConfig{
				DNSOwnIssuer:            true,
				DNSProvider:             "ovh",
				DNSCredentialSecretName: "dns-credentials-ovh-two",
			}),
			want: false,
		},
		{
			name: "flag set but the secret is still the provider-wide one",
			jobs: ownIssuerJob(&LabConfig{
				DNSOwnIssuer:            true,
				DNSProvider:             "ovh",
				ClusterIssuerName:       "easylab-two",
				DNSCredentialSecretName: "dns-credentials-ovh",
			}),
			want: false,
		},
		{
			name: "lab with no config",
			jobs: map[string]*Job{"self": {ID: "self"}},
			want: false,
		},
		{name: "unknown job", jobs: map[string]*Job{}, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			pe := &PulumiExecutor{jobManager: &JobManager{jobs: tt.jobs}}
			assert.Equal(t, tt.want, pe.ownsDNSIssuer("self"))
		})
	}
}

func TestWorkspaceNamespaceShared(t *testing.T) {
	t.Parallel()

	newExecutor := func(jobs map[string]*Job) *PulumiExecutor {
		return &PulumiExecutor{jobManager: &JobManager{jobs: jobs}}
	}
	labOn := func(id, profile, namespace string) *Job {
		return &Job{
			ID:     id,
			Status: JobStatusCompleted,
			Config: &LabConfig{
				StackName:          id,
				UseExistingCluster: true,
				ClusterProfile:     profile,
				WorkspaceNamespace: namespace,
			},
		}
	}

	tests := []struct {
		name string
		jobs map[string]*Job
		want bool
	}{
		{
			name: "another lab in the same namespace",
			jobs: map[string]*Job{
				"self":  labOn("self", "shared", "workshops"),
				"other": labOn("other", "shared", "workshops"),
			},
			want: true,
		},
		{
			name: "another lab in its own namespace",
			jobs: map[string]*Job{
				"self":  labOn("self", "shared", "workshops"),
				"other": labOn("other", "shared", "workshops-two"),
			},
			want: false,
		},
		{
			name: "same namespace but a different cluster",
			jobs: map[string]*Job{
				"self":  labOn("self", "shared", "workshops"),
				"other": labOn("other", "elsewhere", "workshops"),
			},
			want: false,
		},
		{
			name: "alone on the cluster",
			jobs: map[string]*Job{"self": labOn("self", "shared", "workshops")},
			want: false,
		},
		{
			name: "dedicated cluster shares nothing",
			jobs: map[string]*Job{
				"self":  {ID: "self", Status: JobStatusCompleted, Config: &LabConfig{WorkspaceNamespace: "workshops"}},
				"other": labOn("other", "shared", "workshops"),
			},
			want: false,
		},
		{name: "unknown job", jobs: map[string]*Job{}, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, newExecutor(tt.jobs).workspaceNamespaceShared("self"))
		})
	}
}

func TestRenderClusterNotices(t *testing.T) {
	t.Parallel()

	assert.Empty(t, renderClusterNotices(nil))

	html := renderClusterNotices([]string{`namespace set to "workshops-two" & <ok>`})
	assert.Contains(t, html, "This cluster already hosts another lab")
	assert.NotContains(t, html, "<ok>", "notices must be escaped before they reach the page")
	assert.Contains(t, html, "&lt;ok&gt;")
}
