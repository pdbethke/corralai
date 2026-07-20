# Sakana Fugu & Corralai Architectural Analysis

This document records the architectural comparison and analysis of the **Sakana Fugu Technical Report** (Sakana AI, June 2026) and its implications/opportunities for **Corralai** — an audit-by-execution gate for software change, coordinated by a role-differentiated pool of adversarial agents (generator, critic, decorrelated verifier roles).

---

## 1. Overview of Sakana Fugu

Sakana Fugu is a family of LLM orchestrator models designed to coordinate a pool of specialized frontier worker agents (e.g., GPT-5.5, Claude-Opus 4.8, Gemini 3.1 Pro) without parameter-space merging.

### Fugu (Speed & Routing-focused)
* **Inference Efficiency**: Instead of generating text to decide which agent should run next, Fugu uses the hidden states from its backbone model at early token positions, routed through a lightweight prediction head. This outputs logits for $L$ worker models.
* **Low Latency**: This decision-only parametrization completely skips expensive autoregressive decoding for routing decisions.
* **Adaptation**: Fine-tuned via Singular-Value Fine-Tuning (SVFT)—tuning only the singular-value scales of selected weight matrices while keeping orthogonal components fixed.
* **Training**: 
  1. Supervised Fine-Tuning (SFT) over a soft performance distribution (softmax of worker reward metrics) on single-step tasks.
  2. Evolutionary strategies (`sep-CMA-ES`) over multi-turn agentic trajectories to maximize expected terminal reward.

### Fugu-Ultra (Capability & Workflow-focused)
* **Conductor Framework**: Outputs full agentic workflows (subtask strings, worker agent assignments, and access lists linking past responses) using a model trained via Group Relative Policy Optimization (GRPO).
* **Intra-workflow Agent Isolation**: Solves **orchestration collapse** (where early agent actions bias/constrain all downstream agents) by isolating each agent's function-calling trajectory. Agents only observe the actions of other agents through the explicitly configured access lists.
* **Persistent Shared Memory**: Maintains inter-workflow shared memory of tool calls across a multi-turn conversation to prevent redundant tool execution and rediscovery of workspace state.

---

## 2. Structural Comparison

| Dimension | Sakana Fugu (Conductor) | Corralai |
| :--- | :--- | :--- |
| **Orchestration** | Dynamic learned generation of agent subtasks, worker assignments, and context access lists via RL/GRPO. | Deterministic task queue mapped to specialized, decorrelated audit role-agents (generator, mutant-scorer, test-writer, critic). |
| **Re-Planning** | Orchestrator model generates updated/downstream workflows based on step execution feedback. | The gate reacts to measured execution results (kill-rate, exit code) rather than a generated plan — deterministic by construction. |
| **Context Isolation** | Access lists restrict context exposure; intra-workflow isolation avoids orchestration collapse. | Task-level sandboxing. Context is programmatically populated from task descriptions, repo state, and DuckDB shared memory. |
| **Memory** | Inter-workflow shared memory tracks tool calling across a multi-turn conversation. | DuckDB vector/FTS shared memory database, with a human-gated learning loop that compiles lessons into versioned skills. |
| **Security** | Delegated to execution harnesses (Mini-SWE-agent, Terminus 2) with no native sandbox or egress scanning. | Hard sandboxing via unprivileged `bubblewrap` jails, token scrubbing, and pre-push secrets scanning. |

---

## 3. Opportunities for Corralai

Fugu's research highlights several potential enhancements for the Corralai design:

1. **Adversarial Generator-Debugger Loops**:
   Fugu consistently improves scores on Terminal Bench by alternating between GPT-5.5 as a builder and Claude-Opus as a debugger to verify and trace errors. Corralai's `internal/lang` adversarial pool already forces this alternation for certification (a decorrelated critic model, never the generator, scores survivors); the same pattern could extend to locating execution/concurrency bugs during a certify run.

2. **Mitigating Orchestration Collapse**:
   When designing custom sub-plans, Corralai must ensure that agents in separate tasks do not receive the raw execution logs of parallel tasks unless explicitly needed. Maintaining strict task context limits (similar to Fugu's access lists) protects downstream agents from path bias.

3. **Dynamic Specialist Insertion**:
   Fugu excels at routing highly specific tasks to domain experts (e.g., GPT for math-heavy code, Gemini for niche trivia recall). Corralai's LLM Lead could benefit from a routing head or prompt schema that allows it to temporarily override default role mappings (e.g., dynamically swapping the "builder" from Claude to GPT-5.5 for a specific math sub-task).
