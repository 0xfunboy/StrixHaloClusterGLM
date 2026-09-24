# Common platform alignment, 24 September 2026

The four repositories share exactly the source set in `common-core.lock.json`; `tools/verify-common-core.py` verifies local or cross-repository equality. Sources combine GLM's current persistent ownership/native lifecycle and DS41's external controller, configurable reasoning modes and paired native metrics. Both lifecycle URL forms are retained and mutations require authentication plus `confirm:true`. Reasoning mode support is per model configuration; listing a mode is not proof that an engine/template implements it.

Intentional per-model differences: engine/model pins, tensor layouts and kernels, tokenizer/rendering, precision/KV/distribution, model catalog, gateway defaults, lifecycle/deployment configuration, qualification evidence and historical experiments. Existing architecture-specific math/releases are not copied across models. The old Go module name is retained as a shared import identity, not used to choose the active model. Native and external lifecycle drivers are selected explicitly by configuration, not by repository name.

No service deployment, startup, global dependency upgrade, numeric change or automatic frontend orchestration was performed. The new Qwen and MiMo gateway profiles are inference-disabled until their own serving qualification. A common frontend can later enumerate `model-profile.json`, but automatic switching still needs end-to-end lifecycle/resource qualification.

CPU evidence: the shared focused Go/API tests and 62 Node tests pass. The full race-enabled Go suite has nine pre-existing sandbox/namespace-dependent failures reproduced on the unmodified GLM baseline; the integrated tree adds passing tests without introducing new failures. Bubblewrap's NETLINK_ROUTE restriction was not weakened. This limitation is separate from the rootless Podman validator used by Qwen quality campaigns.

Build/test package scope explicitly covers `cmd`, `internal`, `web`, the root `runtime` asset package and `tools`. Incomplete historical Go fixture fragments under `runtime/ds41/daily-fixtures` are retained unchanged as experiment data, not compiled as standalone product packages.
