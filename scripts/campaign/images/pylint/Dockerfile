# Test toolchain for auditing pylint-dev/pylint under certify --repo --jail container.
# The tree is bound in at run time; only the suite's dependencies live here,
# pinned as the project's requirements_test_min.txt pins them.
FROM python:3.14-slim@sha256:0514bc76712d64f8bb48adcb95b1ab0d88c69fee95d62fe4c18ee948bee9b1e1
RUN apt-get update && apt-get install -y --no-install-recommends git && rm -rf /var/lib/apt/lists/*
COPY requirements.txt /tmp/requirements.txt
# Exact versions with hashes: regenerate with pip-compile --generate-hashes from requirements.in.
RUN pip install --no-cache-dir --require-hashes -r /tmp/requirements.txt
