<p align="center">
  <img src="docs/assets/haloclu-header.png" alt="HaloClu — Local coding on paired Strix Halo" width="100%">
</p>

<p align="center">
  <strong>Your models. Your code. Your infrastructure.</strong>
</p>

<p align="center">
  <a href="#get-started">Get started</a> ·
  <a href="#chat">Chat</a> ·
  <a href="#coding-with-pi">Coding</a> ·
  <a href="#models">Models</a> ·
  <a href="#technology">Technology</a> ·
  <a href="#documentation">Documentation</a>
</p>

HaloClu brings local AI chat, Pi-powered development and model operations into
one focused workspace. Built for paired AMD Strix Halo systems, it combines
distributed inference with a lightweight Go gateway and a clean browser interface.

Discuss a problem, attach your files, continue in a coding workspace and review
the changes. Manage model downloads and monitor both machines from the same place.

## At a glance

- **Local AI chat** — streaming responses, document attachments and live token metrics.
- **Pi coding workspaces** — repository tools, protected local edits, independent tests and reviewed Apply.
- **Shared conversations** — persistent Chat/Pi history, explicit handoff and JSON export.
- **Model management** — Hugging Face and ModelScope search, direct links and resumable downloads.
- **Cluster visibility** — paired health, memory, GPU activity and benchmark results.
- **A small footprint** — Go backend, framework-free frontend, grayscale design and responsive layout.

## Get started

On an existing installation, open **[HaloClu](http://127.0.0.1:18093/)**.
Go to **Options → Connection and secrets**, enter the token from
`state/api-token`, and select **Connect**. The browser remembers your session
for 180 days; **Forget** signs it out.

To build and run the gateway against a configured inference backend:

```sh
make build
./bin/strixglm serve --config config.json
```

The reference deployment uses Linux, Go 1.27.1, a provisioned CIRU inference
environment and model weights. Pi workspaces also use Node.js, bubblewrap and
user-systemd scopes. Configure the backend and host paths before starting a new
installation. Follow the [deployment guide](runtime/README.md) and
[Pi setup](WORKSPACES.md) for the complete environment.

On the two reference EVO-X3 hosts, HaloClu includes its controller, operational
fixtures and service definitions in this repository. The provisioned inference
engine lives in `.engine`; weights live in `/home/funboy/models`. Neither is
committed to Git. The retired `ai` and `ai-exp` repositories are not required.
`make` keeps Go workspace/module caches under the ignored `.tools` directory.

[Start and maintain the cluster](runtime/OPERATIONS.md) ·
[Production Ethernet and SSH](deploy/network/README.md)

## Chat

![HaloClu Chat with generation controls, attachments and response metrics](docs/assets/chat.png)

A focused space for technical discussion, writing and code. Responses stream
into readable Markdown with tables, lists and copyable code blocks. Keep
thinking collapsed for a cleaner conversation or expand it as the model works.

Tune reasoning, context window, thinking budget and response length from the
sidebar. Track live decode speed, HTTP throughput, draft acceptance, time to
first token and elapsed request time as you work.

Attach source code, text, Markdown, PDFs or DOCX files. Archive listings and
binary inspection extend the same workflow to project artifacts. The current
GLM deployment uses text extraction, with up to eight files at 32 MiB each.

Conversations are saved on your server. Reopen them later, export JSON or
continue in **Coding / Pi** with the selected transcript as context. Shared
history stays accessible from both pages.

[Chat and conversation guide](docs/CONVERSATIONS.md) · [Generation settings](docs/OPTIONS.md)

## Coding with Pi

![HaloClu Coding workspace with Pi, project connections and independent verification](docs/assets/coding.png)

Bring the Pi coding agent into your browser. HaloClu connects upstream Pi to
your local model and adds project access, verification and review around its
repository workflow.

1. **Connect a project.** Choose a local workspace or a saved Remote SSH host.
2. **Set the task.** Select reasoning and the project's build and test commands.
3. **Work with Pi.** Ask it to inspect code, implement a change or investigate an issue.
4. **Review and apply.** Inspect the diff and independent test results before applying a protected local change.

Protected local mode gives Pi a separate working copy while preserving your
original files. Independent verification runs the configured commands against
the candidate; Apply checks for conflicts and keeps backups of replaced files.
Direct mode is available when you want edits to reach the selected project immediately.

The file browser, diagnostic presets and command terminal keep project
inspection close to the conversation. Agent events and tool output provide a
clear view of work in progress.

Remote SSH uses saved host presets with keys or a connect-time password.
Remote work currently uses Direct mode; protected copies and independent
verification are available for local projects.

[Workspace setup](WORKSPACES.md) · [Protected workflow](docs/PROTECTED_WORKSPACES.md)

## Models

![HaloClu Models with download search and local model inventory](docs/assets/models.png)

Discover models and manage local inventory from one page. **Find & download**
provides source search and direct-link acquisition. **Local & tested** brings
together available checkpoints, architecture notes, runtime compatibility and
recorded test results.

Search Hugging Face or ModelScope, inspect the files and select the
quantization and sidecars you need. Downloads support measured progress,
Pause/Resume, retained partial files and integrity checks against source hashes
when available. A shared refresh keeps the page current.

![HaloClu download controls and resumable jobs](docs/assets/downloads.png)

The catalog makes architecture choices visible: weight formats, PLE/ngram
embeddings, drafting methods and distribution paths are documented alongside
each model. Downloaded files and runtime-qualified configurations have distinct
statuses, helping you choose the next model with the right technical context.

[Download guide](docs/DOWNLOADS.md) · [Model catalog](runtime/model-catalog.json)

## Benchmark

![HaloClu Benchmark with available tests and recorded results](docs/assets/benchmarks.png)

Run the installation's supported benchmarks and inspect the results in one place.
Available operations include API smoke checks, coding throughput, the reference
speed workload and context diagnostics. Each run has an explicit start,
progress reporting and retained results.

[Benchmark guide](benchmarks/README.md) · [Qualification results](QUALIFICATION.md)

## Cluster

![HaloClu Cluster showing paired health, memory and GPU telemetry](docs/assets/cluster.png)

See what both machines are doing. Cluster brings together model identity,
paired health, request activity, context capacity, memory use and GPU telemetry.
The underlying controller manages the two inference ranks as an owned pair,
with documented startup, shutdown and rollback procedures.

[Cluster operations](runtime/OPERATIONS.md)

## Options

![HaloClu Options for interface preferences, authentication and API controls](docs/assets/options.png)

Set your language, text size, layout density, sidebar behavior and thinking
display in one place. English is the default; Italian is available.

Manage remembered browser access, rotate API credentials and control which
API categories accept new work. Connection details and the active model remain
visible alongside those settings. Generation controls stay with Chat and Coding;
global preferences apply across the workspace.

[Options guide](docs/OPTIONS.md)

## Technology

HaloClu's Go gateway serves the browser interface, authenticates API clients,
stores conversations and coordinates downloads and Pi workspaces. The frontend
uses native HTML, CSS and JavaScript. Pi connects through its RPC interface;
the inference runtime remains a separately provisioned service.

This repository, **StrixHaloClusterGLM**, packages HaloClu with its GLM reference
configuration. HaloClu is the shared product identity across model-specific deployments.

| Reference system | Configuration |
| --- | --- |
| Hardware | 2 × GMKtec EVO-X3 · Ryzen AI Max+ 395 · Radeon 8060S |
| Memory | 128 GB UMA per node · 256 GB installed across the pair |
| Model | GLM5.3-Flash-CIRU-STRIX-IU4 · hybrid W4 |
| Distribution | Tensor parallelism TP2 / PP1 · RCCL Socket over USB4 |
| Speculative decoding | DFlash2 · k5 · local-draft0 |
| Coding agent | Pi 0.85.1 · RPC integration |
| Interface | Go gateway · native web frontend · authenticated API |

The recorded 109-input / 256-output reference workload achieved **24.851 decode
tokens/s** and **23.667 HTTP tokens/s**. Workload and measurement details are in
the [reference deployment](docs/REFERENCE_DEPLOYMENT.md).

## Documentation

| Guide | Contents |
| --- | --- |
| [Daily use](docs/DAILY_USE.md) | Chat, coding and everyday workflows |
| [Workspaces](WORKSPACES.md) | Pi installation, local isolation and SSH connections |
| [API and architecture](docs/ARCHITECTURE.md) | Gateway structure and integration points |
| [Runtime deployment](runtime/README.md) | Engine versions, patches and provisioning |
| [Security](SECURITY.md) | Authentication, access boundaries and reporting |
| [Qualification](QUALIFICATION.md) | Test methodology, results and current coverage |
| [Branding](docs/BRANDING.md) | Logos, favicon and social preview configuration |
| [Roadmap](docs/ROADMAP.md) | Planned product development |

## Development

```sh
make test
```

```text
cmd/strixglm/   Application entry point
internal/app/  Gateway, controllers and integrations
web/           Frontend and browser tests
runtime/       Engine manifests, adapters and model catalog
benchmarks/    Benchmark and regression tools
docs/          Product and technical guides
```

See [Contributing](CONTRIBUTING.md) for the development workflow.

Original project code is public; its license is pending selection.
[Third-party notices](THIRD_PARTY_NOTICES.md) cover external components.

## Licensing

Licensing scope and preserved third-party permissions are documented in [LICENSING.md](LICENSING.md). The [0xfunboy Non-Commercial License](LICENSE.md) covers eligible original material only.
