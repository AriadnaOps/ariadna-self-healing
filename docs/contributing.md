# Contributing

Thank you for your interest in contributing to the Ariadna Self-Healing Operator. This page summarizes how to get started. For full details, see [CONTRIBUTING.md](https://github.com/ariadna-ops/ariadna-self-healing/blob/main/CONTRIBUTING.md) in the repository.

## Prerequisites

- **Go 1.25+** (see [go.mod](https://github.com/ariadna-ops/ariadna-self-healing/blob/main/go.mod))
- **Docker** (optional, for image builds)
- **kubectl** and a Kubernetes cluster (optional, for deployment and e2e)
- **[controller-gen](https://github.com/kubernetes-sigs/controller-tools)** for CRD generation

## Documentation

**Keep documentation in sync with code changes.** When you update code (APIs, behavior, config, CRDs), update:

- README.md, docs/ (MkDocs), charts/selfhealing-operator/README.md, and code comments.

## Build and test

```bash
make build
make test
make verify   # fmt, vet, lint, test
```

## Code generation

If you change API types under `api/`:

```bash
make generate   # DeepCopy and generated code
make manifests  # CRD YAMLs in config/crd/bases
```

CRDs under `config/crd/bases/` are committed; CI checks that `make manifests` does not change them.

## Submitting changes

- **Bug reports and feature requests**: Open an issue describing the problem or proposal.
- **Pull requests**: Target the default branch. Ensure `make verify` passes.

## Commit messages

We use [Conventional Commits](https://www.conventionalcommits.org/) (e.g. `feat:`, `fix:`, `docs:`, `chore:`).
