---
icon: lucide/layout-template
title: Templates
---
# Workspace template examples

A lab's **workspace templates** describe the environments students can request.
Each template is one workspace flavor: an image, an optional git repo to
clone, resources, and anything extra the workshop needs (a database, a Docker
daemon, some VS Code extensions).

This page is a cookbook of complete, copy-pasteable examples. For the field-by-field
reference and the wizard form, see [Configure workspaces](admin.md#configure-workspaces).

## Using these examples

1. Go to **New Lab** and walk through to the **Templates** step.
2. Set **Configuration Mode** to **YAML**.
3. Paste an example below (or save it as a `.yaml` file and use **Upload file**).
4. Click **Validate** — it checks the document without creating anything.
5. Finish the wizard and click **Create Lab**.

!!! warning "YAML wins while it is selected"
    While **YAML** mode is selected, the form fields are ignored when the lab is
    created. Switching back to **Form** does *not* carry your YAML edits over.

Only `name` is required, and template names must be unique within a lab. Unknown
keys are **rejected** rather than ignored, so a typo like `imagee:` fails
validation instead of silently shipping a lab without its image.

## Key reference

| Key | Type | Notes |
|---|---|---|
| `name` | string | **Required.** Shown in the student template selector; unique per lab. |
| `ide` | string | **Legacy — omit it.** Workspaces always run code-server. Older labs carrying `openvscode` still load; the value is rewritten to `code-server`. |
| `image` | string | Container image override. Defaults to `codercom/code-server:latest`. |
| `git_repo` | string | Cloned into the workspace on first start. **Implies a 5Gi volume** when `disk_size` is unset. |
| `git_branch` | string | Clones a single branch. Default branch when unset. |
| `git_folder` | string | Subfolder of the repo the IDE opens. Repo root when unset. |
| `cpu` | string | Resource request, e.g. `500m`. **Quote plain numbers**: `"2"`. |
| `memory` | string | Resource request, e.g. `4Gi`. |
| `disk_size` | string | Volume size, e.g. `10Gi`. **Empty means no volume** — the workspace is ephemeral. |
| `startup_script` | string | Shell commands run before the IDE starts. Best-effort. |
| `dotfiles_repo` | string | Cloned to `~/.dotfiles`; its `install.sh` / `setup.sh` / `bootstrap.sh` runs if present. |
| `extensions` | list | VS Code extension IDs installed on start. |
| `env` | map | Environment variables for the workspace container. |
| `sidecars` | list | Extra containers in the pod: `name`, `image`, `ports`, `env`, `privileged`, `capabilities`. |
| `mounts` | list | Existing ConfigMaps/Secrets: `type` (`configmap` \| `secret`), `name`, `path`. Mounted **read-only**. |
| `image_pull_secrets` | list | Names of registry credentials used to pull this template's images. See [Private registries and repositories](#private-registries-and-repositories). |
| `git_auth_secret` | string | Name of a git credential used to clone a private `git_repo`. **http(s) only.** See [Private registries and repositories](#private-registries-and-repositories). |
| `devcontainer` | map | Builds the image from the repo's `devcontainer.json` instead of using `image`. See [Devcontainer workshops](#devcontainer-workshops). |

## Minimal

The smallest valid document. Students get a code-server workspace on the
default image, with no persistent volume.

```yaml
workspace_templates:
  - name: default
```

!!! info "No `disk_size` means nothing is kept"
    Without `disk_size` (and without `git_repo`, which implies one), the workspace
    has no volume: anything the student writes is lost if the pod restarts. Set a
    `disk_size` for any workshop where students build up work over a session.

## A git-backed workshop

The most common setup: clone the exercises repo, open a subfolder, install the
language extension. `git_repo` provisions a 5Gi volume automatically, so student
work survives a pod restart.

```yaml
workspace_templates:
  - name: go-workshop
    git_repo: https://gitlab.com/yodamad/go-workshop.git
    git_branch: main
    git_folder: exercises
    extensions:
      - golang.go
```

The repo is cloned **only when the volume is empty** — a restarting workspace keeps
the student's own commits and edits rather than being reset.

## Sizing a workspace

```yaml
workspace_templates:
  - name: heavy
    cpu: "2"
    memory: 4Gi
    disk_size: 20Gi
```

!!! danger "Quote bare numbers"
    `cpu: 2` fails validation — these fields are strings. Write `cpu: "2"`.
    Values with a unit suffix (`500m`, `4Gi`) are already strings and need no quotes.

## Several templates in one lab

Students request **one workspace per template**, so a lab with three templates lets
each student run up to three workspaces side by side. They pick from a dropdown on
the student portal.

```yaml
workspace_templates:
  - name: go
    git_repo: https://gitlab.com/yodamad/go-workshop.git
    extensions:
      - golang.go

  - name: node
    git_repo: https://gitlab.com/yodamad/node-workshop.git
    extensions:
      - dbaeumer.vscode-eslint

  - name: scratch
    disk_size: 5Gi
```

## Installing system packages

Startup scripts run as the workspace user, which has passwordless `sudo`, so
packages can be installed at start:

```yaml
workspace_templates:
  - name: tooling
    startup_script: |
      sudo apt-get update
      sudo apt-get install -y jq make httpie
```

Startup scripts are **best-effort**: a failing command is visible in
`kubectl logs` but never blocks the workspace from opening.

Every student pays this cost on every workspace start, so for anything slow or
large bake the tools into an image instead and point the template at it:

```dockerfile
FROM codercom/code-server:latest
USER root
RUN apt-get update && apt-get install -y golang nodejs && rm -rf /var/lib/apt/lists/*
USER coder
```

```yaml
workspace_templates:
  - name: custom
    image: registry.example.com/easylab/go-workshop:1.0
```

Push it to a registry the lab cluster can pull from. Prebuilt images also start
faster than installing the same packages in every student's workspace. If the
registry is private, add `image_pull_secrets` — see
[Private registries and repositories](#private-registries-and-repositories).

## A database sidecar

Sidecars are extra containers in the same pod, reachable from the IDE at
`localhost:<port>`.

```yaml
workspace_templates:
  - name: api-workshop
    git_repo: https://gitlab.com/yodamad/api-workshop.git
    env:
      DATABASE_URL: postgres://postgres:postgres@localhost:5432/app
    sidecars:
      - name: db
        image: postgres:16
        ports: [5432]
        env:
          POSTGRES_PASSWORD: postgres
          POSTGRES_DB: app
```

Each student gets their **own** database — sidecars live in the student's pod, not
in a shared service.

!!! info "`workspace` is reserved"
    The IDE container is named `workspace`. A sidecar with that name (or a name
    that isn't a valid DNS label) is dropped from the pod.

## Docker inside a workspace

Managed clusters (OVHcloud, AKS) run **containerd**, not Docker, so there is no host
Docker socket to reuse. Run a Docker-in-Docker sidecar and point the CLI at it:

```yaml
workspace_templates:
  - name: docker-workshop
    env:
      DOCKER_HOST: tcp://localhost:2375
    startup_script: |
      sudo apt-get update && sudo apt-get install -y docker.io
      until docker info >/dev/null 2>&1; do sleep 2; done
    sidecars:
      - name: docker
        image: docker:dind
        ports: [2375]
        privileged: true
        env:
          DOCKER_TLS_CERTDIR: ""
```

`DOCKER_TLS_CERTDIR: ""` disables TLS on the daemon, which is what makes the plain
`tcp://localhost:2375` connection work. The startup script waits for the daemon so
students don't hit "cannot connect" in their first minute.

!!! danger "`privileged: true` escalates to the node"
    Docker-in-Docker requires a privileged container, and a privileged container can
    take over the node it runs on. Only enable this on a trusted workshop cluster you
    control. For an unprivileged alternative, use `docker:dind-rootless` with the
    right `capabilities`, or install a Sysbox/Kata RuntimeClass on the cluster.

## Mounting config and secrets

Mount a ConfigMap or Secret that **already exists** in the workspace namespace
(`workshops` by default — set it on the **Workspace** step):

```yaml
workspace_templates:
  - name: preconfigured
    mounts:
      - type: configmap
        name: workshop-config
        path: /etc/workshop
      - type: secret
        name: workshop-registry
        path: /home/coder/.docker
```

Create them before students request workspaces:

```bash
kubectl -n workshops create configmap workshop-config --from-file=./config/
kubectl -n workshops create secret generic workshop-registry --from-file=.dockerconfigjson=./config.json
```

!!! warning "The object must exist first"
    A mount referencing a missing ConfigMap or Secret leaves the pod stuck and the
    workspace never opens. Mounts are read-only, so students can't edit them.

## Personal dotfiles

```yaml
workspace_templates:
  - name: dotfiles
    dotfiles_repo: https://github.com/yodamad/dotfiles
```

The repo is cloned to `~/.dotfiles` and the first executable of `install.sh`,
`setup.sh` or `bootstrap.sh` runs. Like startup scripts, this is best-effort.

## Everything together

A full-featured template using most of the schema:

```yaml
workspace_templates:
  - name: full-stack
    image: codercom/enterprise-base:ubuntu
    git_repo: https://gitlab.com/yodamad/workshop.git
    git_branch: main
    git_folder: exercises
    cpu: "2"
    memory: 4Gi
    disk_size: 10Gi
    startup_script: |
      sudo apt-get update && sudo apt-get install -y jq
      make bootstrap
    dotfiles_repo: https://github.com/yodamad/dotfiles
    extensions:
      - golang.go
      - dbaeumer.vscode-eslint
    env:
      DATABASE_URL: postgres://postgres:postgres@localhost:5432/app
    sidecars:
      - name: db
        image: postgres:16
        ports: [5432]
        env:
          POSTGRES_PASSWORD: postgres
          POSTGRES_DB: app
    mounts:
      - type: configmap
        name: workshop-config
        path: /etc/workshop
```

## Private registries and repositories

A private image or a private workshop repository needs a credential. Credentials
are never written in the template: the template names a Kubernetes Secret, and the
Secret lives in the lab's cluster. That is what keeps tokens out of the lab's job
file and out of the templates export, so a template is always safe to share.

```yaml
workspace_templates:
  - name: private-workshop
    image: registry.example.com/workshops/base:1
    image_pull_secrets:
      - regcred
    git_repo: https://gitlab.com/org/private-workshop.git
    git_auth_secret: gitcred
    disk_size: 10Gi
```

### Adding a credential

There are two moments to add one, and they behave the same way — the token is
written to the lab's cluster and EasyLab keeps no copy:

- **In the creation wizard.** The **Workspace Templates** step has a *Credentials*
  section. Add a row for each credential your templates reference. Because the
  cluster does not exist yet, the tokens are held in memory and written the moment
  provisioning finishes — before the lab reports itself ready.
- **On the lab's Workspaces page**, once the lab is up. Expand **Credentials**, add
  a *Container registry* or *Git repository* credential, and paste the token.

Saving over an existing name **rotates** it. Workspaces already running keep the
old token — the kubelet resolved it when the pod started — so a rotation reaches
students only as their workspaces are recreated.

Credentials live in the cluster, so **destroying a lab destroys them**. Recreating
a lab prompts you to re-enter the tokens (the names and types are carried over). A
retried lab keeps them.

If a template references a credential the cluster does not have — a token lost to
a server restart mid-provisioning, a name typo, or one not yet added — the
Credentials panel says so, and the workspaces that need it fail until it is added.

If you have `kubectl` access you can create the same Secrets by hand; the panel
lists those too, since what matters is the name your template references:

```bash
# Registry — for image_pull_secrets
kubectl create secret docker-registry regcred \
  --docker-server=registry.example.com \
  --docker-username=<user> \
  --docker-password=<token> \
  --namespace=workshops

# Git — for git_auth_secret
kubectl create secret generic gitcred \
  --type=kubernetes.io/basic-auth \
  --from-literal=username=oauth2 \
  --from-literal=password=<token> \
  --namespace=workshops
```

!!! warning "`image_pull_secrets` does not cover the devcontainer build"
    Two different things pull images, and they do not share credentials.

    | Image | Pulled by | Credential |
    |---|---|---|
    | `image`, `sidecars`, the init containers | The kubelet | `image_pull_secrets` |
    | The devcontainer's base image, `fallback_image`, `cache_repo` | The build, inside the pod | `devcontainer.registry_auth_secret` |

    A devcontainer template pulling a private base image needs
    `registry_auth_secret`; `image_pull_secrets` will not help it. Both may be set,
    and both may name the same Secret.

!!! note "`git_auth_secret` is http(s) only"
    A username and password is not how `ssh://` authenticates, so the secret would
    be ignored and the clone would fail on the host key instead. Validation rejects
    the combination rather than letting you find out at the first workspace.

    For GitLab use `oauth2` as the username with a personal access token; GitHub
    accepts any non-empty username. The Credentials panel fills in `oauth2` when
    you leave it blank.

!!! danger "Tokens in a repo URL are not private"
    `git_repo: https://oauth2:glpat-xxx@gitlab.com/org/repo.git` works, and EasyLab
    accepts it with a warning rather than rejecting it. But the URL is stored in
    the lab's job file, returned by the jobs API, and **included in the templates
    export** — so the token travels anywhere the template does. `git_auth_secret`
    exists to avoid exactly that.

### What students can and cannot do

The clone is authenticated; the student's IDE is not. The token is given to the
clone step alone and never to the workspace container, because students have a
shell there and the token is yours, not theirs.

So a student's checkout has an ordinary remote with no stored credentials: reading
the code works, but an interactive `git pull` or `git push` against the private
repo will prompt and fail. If a workshop needs students to push, have them
authenticate as themselves from inside the workspace.

## Devcontainer workshops

When a workshop repository already ships a `.devcontainer/devcontainer.json`, a
template can build from it instead of naming an `image`. Each student's workspace
builds the devcontainer on first start, so a workshop's own `Dockerfile` and
`features` work as written.

```yaml
workspace_templates:
  - name: go-workshop
    git_repo: https://gitlab.com/org/workshop.git
    git_branch: main
    cpu: "2"
    memory: 4Gi
    disk_size: 20Gi
    extensions:
      - golang.go
    devcontainer:
      enabled: true
      dir: .devcontainer
      cache_repo: registry.example.com/easylab/cache
      registry_auth_secret: regcred
```

| Key | Type | Notes |
|---|---|---|
| `enabled` | bool | Turns the mode on. **Requires `git_repo`**, and **conflicts with `image`**. |
| `dir` | string | Folder holding `devcontainer.json`. Defaults to `.devcontainer`. |
| `cache_repo` | string | **Required.** Registry the built layers are cached in — see below. |
| `registry_auth_secret` | string | Registry credential for **everything the build pulls or pushes**: the base image, `fallback_image`, and `cache_repo`. Omit for public registries. See [Private registries and repositories](#private-registries-and-repositories). |
| `fallback_image` | string | Used when the devcontainer names neither an image nor a Dockerfile. |
| `insecure` | bool | Skip TLS verification when cloning and pulling. |

The template's own `ide` key applies here too — see the constraints below.

The image the devcontainer builds contains no IDE of its own, so code-server is
copied onto a volume the build is told to leave alone, and started from there.
That makes the IDE independent of the devcontainer's own base image — but it
does impose three constraints:

- **The base image must be glibc-based.** The bundle ships a dynamically
  linked Node, which cannot run on Alpine/musl. The failure is misleading: the
  workspace container exits with `no such file or directory` even though the
  binary is plainly there.
- **The devcontainer's user needs a writable `$HOME`.** code-server stores its
  user data and extensions under the home directory of the user the
  devcontainer runs as. When that is unwritable, extension installs are skipped
  quietly and the IDE then fails to start — so an unexplained "my extensions
  are missing" is worth checking here first.
- **Set `disk_size`.** It provisions the volume holding the student's files,
  which is what makes the workspace folder writable by the usual uid-1000
  devcontainer users (`vscode`, `node`, `coder`). Without it, the folder can end
  up owned by root and the student cannot save.

Authentication is the same as outside devcontainer mode: code-server presents a
login page taking the workspace password.

!!! note "A private devcontainer repo needs `git_auth_secret`"
    The devcontainer is built inside the pod, and the builder clones the repo
    itself — so a private `git_repo` needs `git_auth_secret`, exactly like any other
    private repo. It is **separate** from `registry_auth_secret`, which only covers
    the images the build pulls and pushes. Miss it and the clone falls back to no
    authentication: the build then reports the `devcontainer.json` as missing
    (`no such file or directory`) because nothing was ever fetched.

    ```yaml
    workspace_templates:
      - name: go-workshop
        git_repo: https://innersource.example.com/org/workshop.git
        git_branch: master
        git_auth_secret: gitcred
        disk_size: 20Gi
        devcontainer:
          enabled: true
          dir: .devcontainer
          cache_repo: registry.example.com/easylab/cache
          registry_auth_secret: regcred
    ```

    Create `gitcred` the same way as for any private repo — see
    [Private registries and repositories](#private-registries-and-repositories).

### Importing a devcontainer

Rather than writing the block by hand, use **Import from devcontainer** in the
Templates step. It reads the repo's `devcontainer.json` — by cloning the repo, or
from a `devcontainer.json` / repository `.zip` you upload — fills the template in,
and reports anything in the devcontainer that will not take effect. The result is
ordinary YAML: review and edit it before creating the lab.

For a private repository, supply an **access token** in the import dialog. It
authenticates that one clone and is then discarded — it is not saved anywhere and
does not become the lab's `git_auth_secret`. It cannot: at import time the lab's
cluster does not exist yet, so there is nowhere to put a Secret. Giving *students*
access to the repo is the separate step described in
[Private registries and repositories](#private-registries-and-repositories).

### What is honoured, and what is not

The build understands the parts that describe the image; EasyLab covers several
keys the build ignores; and some keys are honoured by neither.

| devcontainer.json | Handled by |
|---|---|
| `image`, `build.dockerfile`, `build.args`, `build.context` | The build |
| `features` | The build |
| `containerEnv`, `remoteEnv` | The build |
| `postCreateCommand`, `onCreateCommand`, `updateContentCommand` | The build |
| `customizations.vscode.extensions` | **EasyLab** — copied to `extensions` on import |
| `hostRequirements.cpus` / `memory` / `storage` | **EasyLab** — copied to `cpu` / `memory` / `disk_size` on import |
| `workspaceFolder` | **EasyLab** — copied to `git_folder` when relative |
| `dockerComposeFile`, `service`, `runServices` | **Nobody** — rejected at import |
| `forwardPorts`, `mounts`, `workspaceMount` | **Nobody** — only the IDE port is routed; use the template's `mounts` |
| `privileged`, `capAdd`, `init` | **Nobody** — use a template `sidecar` instead |
| `postStartCommand` | **Nobody** — move it to `postCreateCommand` or the template's `startup_script` |

!!! warning "docker-compose devcontainers are not supported"
    A devcontainer built on `dockerComposeFile` cannot be used: the workspace is
    built as a single image, not a set of orchestrated services. The import
    rejects it outright. The nearest equivalent is a hand-written `sidecars:`
    block — see [A database sidecar](#a-database-sidecar).

!!! danger "The cache registry is not optional"
    `cache_repo` is required for a reason. Image layers are cached there and
    pushed after the first build, so the first workspace pays for the build and
    every later one starts from the cache — the difference is minutes versus
    seconds, *per student*. Without it, all thirty students in a workshop would
    each rebuild the whole devcontainer from scratch.

    The Secret named by `registry_auth_secret` must already exist in the workspace
    namespace — add it from the lab's **Credentials** panel, or with `kubectl`.
    See [Private registries and repositories](#private-registries-and-repositories).

!!! tip "First start is slow, and it uses node disk"
    Even with a warm cache the first workspace of a lab builds the devcontainer
    before the IDE appears, which can take several minutes. The build writes to
    the node's disk rather than the workspace volume, so a large image across
    many students is worth sizing the node pool for.

## Reusing a lab's templates

Use **Export Templates YAML** on the [labs list](admin.md#manage-your-labs) to
download an existing lab's `workspace-templates-<stack>.yaml`, then load it into a
new lab with **Upload file**. Only the templates are exported — credentials are
never included.

This makes template documents easy to keep in a git repository and reuse from one
workshop edition to the next.

## Troubleshooting

| Symptom | Likely cause |
|---|---|
| `cannot unmarshal number ... into ... of type string` | A bare number where a string is expected — quote it (`cpu: "2"`). |
| `unknown field "imagee"` | A typo'd key. Unknown keys are rejected on purpose. |
| `duplicate template name "go"` | Two templates share a `name`; names must be unique within a lab. |
| Student work disappears after a restart | No `disk_size` — the workspace has no volume. |
| The repo isn't cloned | `git_repo` clones only into an **empty** volume; an existing workspace keeps its contents. |
| Workspace never opens | Often a `mounts` entry pointing at a ConfigMap/Secret that doesn't exist in the namespace. |
| `apt-get` fails in the startup script | Prefix it with `sudo`; the workspace user has passwordless sudo but is not root. |
| A sidecar is missing from the pod | It was named `workspace` (reserved) or has no `image`. |
| `devcontainer.cache_repo is required` | Devcontainer mode needs a layer cache registry; see [Devcontainer workshops](#devcontainer-workshops). |
| `devcontainer.enabled conflicts with image` | The image is built from the repo's `devcontainer.json` — remove `image`. |
| `devcontainer.enabled requires git_repo` | The `devcontainer.json` is read from the workshop repo, so there must be one. |
| `git_auth_secret requires git_repo` | The credential has nothing to authenticate to — remove it, or add the repo. |
| `git_auth_secret needs an http(s) git_repo` | A username and password is not how `ssh://` authenticates. Use an `https://` remote. |
| `failed to read git auth secret "x"` | The template names a credential the lab's cluster does not have. Add it in **Credentials** on the Workspaces page. |
| Workspace pods stuck in `ImagePullBackOff` | A private image with no `image_pull_secrets`, or a name that does not match a credential. |
| The devcontainer build fails pulling its base image | Devcontainer images are pulled by the build, not the kubelet — they need `devcontainer.registry_auth_secret`, not `image_pull_secrets`. |
| Devcontainer build fails with `devcontainer.json: no such file or directory` on a private repo | The clone ran with no credentials (`Using no authentication!` in the pod logs), so nothing was fetched. Add `git_auth_secret` — see [Devcontainer workshops](#devcontainer-workshops). Recreate the workspace so the clone runs again on a clean volume. |
| Students can read the repo but cannot `git push` | Expected. The token authenticates the clone only and is never given to the IDE. |
| A devcontainer import fails on `dockerComposeFile` | Compose-based devcontainers are not supported — use `sidecars` instead. |
| Every student's workspace takes minutes to start | The cache registry is unreachable or unwritable, so each build starts cold. Check `registry_auth_secret`. |
