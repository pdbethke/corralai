# corralai demo — one-command, role-differentiated local-LLM swarm.
COMPOSE = docker compose -f docker-compose.yml

# --- multi-model demo knobs ---
# The two finding-FILERS run on DIFFERENT models so model_comparison shows an A-vs-B.
# Both default to local Ollama — demo-models works with NO API key.
# Group A (pentester): qwen2.5-coder:7b (the same model the swarm uses everywhere).
MODELS_BACKEND_A ?= ollama
MODELS_MODEL_A   ?= qwen2.5-coder:7b
# Group B (reviewer): llama3.2:3b — a distinct local model for the A-vs-B split.
# builder + tester are NOT in this group; they always use qwen2.5-coder:7b.
MODELS_BACKEND_B ?= ollama
MODELS_MODEL_B   ?= llama3.2:3b
# CORRALAI_ROLE_MODELS carries the plan to the brain for topology drift + apply-on-spawn.
# pentester → Group A, reviewer → Group B: the two finding-filers on different models.
# builder + tester always use qwen2.5-coder:7b (they don't file findings).
MODELS_ROLE_MODELS = pentester=$(MODELS_BACKEND_A):$(MODELS_MODEL_A),reviewer=$(MODELS_BACKEND_B):$(MODELS_MODEL_B),builder=ollama:qwen2.5-coder:7b,tester=ollama:qwen2.5-coder:7b

.PHONY: demo demo-cpu demo-clobber demo-observe demo-mission demo-mission-epic demo-models down \
        logs logs-mission logs-models

demo: ## brain + builder/tester/pentester/reviewer coordinating — open http://localhost:9019
	@echo ""
	@echo "==> Open http://localhost:9019 (Activity tab) once the stack is ready."
	@echo "==> First run pulls qwen2.5-coder:7b (~4.7 GB) — allow ~3-10 min."
	@echo "==> Watch the pull: docker compose -f docker-compose.yml logs -f ollama-pull"
	@echo ""
	$(COMPOSE) --profile coordinated up --build

demo-cpu: ## coordinated swarm on CPU (no NVIDIA GPU required — slower, works anywhere)
	@echo ""
	@echo "==> CPU mode: GPU reservation removed. Model runs on CPU (slower)."
	@echo "==> Open http://localhost:9019 once ready."
	@echo "==> First run pulls qwen2.5-coder:7b (~4.7 GB) — allow ~10-30 min on CPU."
	@echo ""
	docker compose -f docker-compose.yml -f docker-compose.cpu.yml --profile coordinated up --build

demo-clobber: ## same brain + UI, but agents IGNORE conflicts and trample the shared files
	$(COMPOSE) --profile clobber up --build

demo-observe: ## the coordinated swarm PLUS the read-only observer — watch at http://localhost:9020
	$(COMPOSE) --profile coordinated --profile observe up --build

demo-mission: ## the FULL adaptive loop: a directive -> hive builds + re-plans it. Watch the Progress tab at http://localhost:9019
	@echo ""
	@echo "==> Open http://localhost:9019 → Progress tab once the stack is ready."
	@echo "==> First run pulls qwen2.5-coder:7b (~4.7 GB) — allow ~3-10 min."
	@echo "==> Watch the pull: docker compose -f docker-compose.yml logs -f ollama-pull"
	@echo ""
	$(COMPOSE) --profile mission up --build

demo-mission-epic: ## demo-mission with the AMBITIOUS directive (recursive-descent calc parser) — takes much longer on the default local 7B model; shines on a frontier model
	@echo ""
	@echo "==> Epic directive: a full arithmetic-expression parser. On the default local"
	@echo "==> 7B model this takes a while — point at a frontier model to see it fly"
	@echo "==> (see 'Bring your own model' in the README)."
	@echo ""
	DEMO_DIRECTIVE="Build a Go package 'calc' that evaluates arithmetic expression strings (a recursive-descent parser supporting + - * / and parentheses) with table-driven unit tests; make 'go build ./...' and 'go test ./...' pass" \
	$(COMPOSE) --profile mission up --build

demo-models: ## multi-model comparison: pentester on qwen (Group A), reviewer on llama3.2:3b (Group B). Key-free. Populates model_comparison report.
	@echo ""
	@echo "==> demo-models: key-free A-vs-B (qwen2.5-coder:7b vs llama3.2:3b)."
	@echo "==> Open http://localhost:9019 → Topology tab for the model_comparison table."
	@echo "==> First run pulls both models (~4.7 GB + ~2.0 GB) — allow ~5-15 min."
	@echo "==> Watch: docker compose -f docker-compose.yml logs -f ollama-pull models-ollama-pull"
	@echo ""
	CORRALAI_ROLE_MODELS="$(MODELS_ROLE_MODELS)" \
	MODELS_BACKEND_A="$(MODELS_BACKEND_A)" MODELS_MODEL_A="$(MODELS_MODEL_A)" \
	MODELS_BACKEND_B="$(MODELS_BACKEND_B)" MODELS_MODEL_B="$(MODELS_MODEL_B)" \
	$(COMPOSE) --profile models up --build

down: ## stop everything and remove the workspace volume
	$(COMPOSE) --profile coordinated --profile clobber --profile observe --profile mission --profile models down -v

logs: ## follow the coordinated swarm logs (builder/tester/pentester/reviewer)
	$(COMPOSE) logs -f builder tester pentester reviewer

logs-mission: ## follow the mission-profile agent logs
	$(COMPOSE) logs -f mission-builder mission-tester mission-pentester mission-reviewer mission-lead mission-client

logs-models: ## follow the models-profile agent logs
	$(COMPOSE) logs -f models-pentester models-reviewer models-builder models-tester models-lead
