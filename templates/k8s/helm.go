package k8s

import (
	k8s "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes"
	v1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/core/v1"
	helmv3 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/helm/v3"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

type HelmChartInfo struct {
	Name            string
	ChartName       string
	Version         string
	Url             string
	crds            bool `default:"false"`
	createNamespace bool `default:"false"`
	Values          pulumi.Map
}

func InitHelm(ctx *pulumi.Context, provider *k8s.Provider, chart HelmChartInfo, namespace *v1.Namespace) (*helmv3.Release, error) {

	// Use Helm to install
	helmRelease, err := helmv3.NewRelease(ctx, chart.Name, &helmv3.ReleaseArgs{
		Chart:           pulumi.String(chart.ChartName),
		Namespace:       namespace.Metadata.Name(),
		CreateNamespace: pulumi.Bool(chart.createNamespace),
		RepositoryOpts: &helmv3.RepositoryOptsArgs{
			Repo: pulumi.String(chart.Url),
		},
		SkipCrds: pulumi.Bool(chart.crds),
		Version:  pulumi.String(chart.Version),
		Values:   chart.Values,
		Timeout:  pulumi.Int(900), // 15 minutes timeout
	}, pulumi.Provider(provider), pulumi.DependsOn([]pulumi.Resource{namespace}))
	if err != nil {
		return nil, err
	}
	return helmRelease, nil
}
