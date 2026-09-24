# StrixHaloClusterGLM

Model-family identifier: `glm53-flash`. Canonical repository entry: `model-profile.json`. Default `config.json` is now model-specific. Engine reference: `runtime/cluster.json`.

existing GLM lifecycle; no new engine qualification. Existing model release files, runtimes, weights, closed campaign protocols and raw evidence are unchanged. Shared API/frontend/ownership source is governed by `common-core.lock.json`. No service was installed/restarted and no model switch was performed during this reorganization.

Both legacy lifecycle API paths remain supported with authenticated explicit confirmation. A future multi-model frontend must coordinate the single shared resource group; separate repositories do not make the two-node resident coexist with a local Qwen instance.
