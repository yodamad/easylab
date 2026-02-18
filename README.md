<p align="center">
  <img src="https://raw.githubusercontent.com/yodamad/easylab/main/assets/logo.png" alt="EasyLab Logo" width="200"/>
</p>

<h1 align="center">EasyLab</h1>

<p align="center">
  <strong>Cloud Infrastructure Lab Management Made Easy</strong>
</p>

<p align="center">
  <a href="https://go.dev/"><img src="https://img.shields.io/badge/Go-1.25+-00ADD8?style=flat&logo=go&logoColor=white" alt="Go Version"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/License-MIT-blue.svg" alt="License"></a>
</p>

<p align="center">
  <a href="#-quick-start">Quick Start</a> •
  <a href="#-features">Features</a> •
  <a href="#-documentation">Documentation</a> •
  <a href="#-contributing">Contributing</a>
</p>

---

EasyLab streamlines cloud infrastructure lab management for **educators**, **workshop organizers**, and **DevOps teams**. It automates provisioning of Kubernetes clusters and development workspaces based on [Coder](https://coder.com/) on any cloud with [Pulumi](https://www.pulumi.com/).

📑 **[Full documentation](https://docs.easylab.yodamad.fr)** • Currently supports **OVHcloud** (more providers coming soon)

![EasyLab Homepage](https://raw.githubusercontent.com/yodamad/easylab/main/assets/homepage.png)

## ✨ Features

| Admin | Student |
|-------|---------|
| Lab creation & deployment | Workspace requests |
| OVHcloud integration | Lab catalog |
| Job management & logs | Session management |
| Kubeconfig access | Self-service onboarding |

## 🚀 Quick Start

```bash
curl -fsSL https://raw.githubusercontent.com/yodamad/easylab/main/docker-compose.yml -o docker-compose.yml
export LAB_ADMIN_PASSWORD="your-secure-password"
export LAB_STUDENT_PASSWORD="your-student-password"
docker-compose up -d
# Access at http://localhost:8080
```

See the [documentation](https://docs.easylab.yodamad.fr) for [Docker](https://docs.easylab.yodamad.fr/docker/), [Kubernetes](https://docs.easylab.yodamad.fr/k8s/), and local development setup.

## 📚 Documentation

| Resource | Description |
|----------|-------------|
| [docs.easylab.yodamad.fr](https://docs.easylab.yodamad.fr) | Admin, student, deployment & OVHcloud guides |
| [TESTING.md](TESTING.md) | Testing documentation |
| [COVERAGE_SETUP.md](COVERAGE_SETUP.md) | Code coverage setup |

## 🤝 Contributing

1. **Fork** the repository
2. **Create** a feature branch (`git checkout -b feature/amazing-feature`)
3. **Commit** your changes (`git commit -m 'Add amazing feature'`)
4. **Push** and open a Merge Request

## 📄 License

MIT License — see [LICENSE](LICENSE) for details.

---

<p align="center">
  Built with <a href="https://www.ovhcloud.com/">OVHcloud</a>, <a href="https://www.pulumi.com/">Pulumi</a>, <a href="https://coder.com/">Coder</a>, <a href="https://go.dev/">Go</a>, <a href="https://kubernetes.io/">Kubernetes</a>
</p>
