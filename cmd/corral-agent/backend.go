// SPDX-License-Identifier: Elastic-2.0

package main

// The tool-calling LLM backends (ollama/openai/anthropic), the Backend/
// ModelSwitcher interfaces, and the creds-store secret plumbing used to live
// here unexported. They now live in the importable internal/agentbackend
// package (unblocks `corral certify --local`, which needs to construct the
// exact same backends). This file keeps the local names corral-agent's other
// files already use (Backend, modelSwitcher, omsg, otoolcal,
// ErrModelUnreachable, agentSecret, scrubSecretEnv) as thin aliases/wrappers
// so nothing else in this package had to change.

import "github.com/pdbethke/corralai/internal/agentbackend"

// Backend is corral-agent's name for agentbackend.Backend.
type Backend = agentbackend.Backend

// modelSwitcher is corral-agent's name for agentbackend.ModelSwitcher.
type modelSwitcher = agentbackend.ModelSwitcher

// omsg/otoolcal are corral-agent's names for agentbackend.Message/ToolCall —
// the message/tool-call shapes every Backend implementation speaks.
type omsg = agentbackend.Message
type otoolcal = agentbackend.ToolCall

// ErrModelUnreachable re-exports agentbackend.ErrModelUnreachable under the
// name every call site in this package already uses.
var ErrModelUnreachable = agentbackend.ErrModelUnreachable

// agentSecret re-exports agentbackend.Secret.
func agentSecret(name string) string { return agentbackend.Secret(name) }

// scrubSecretEnv re-exports agentbackend.ScrubSecretEnv.
func scrubSecretEnv() { agentbackend.ScrubSecretEnv() }
