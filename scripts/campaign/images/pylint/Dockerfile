# Test toolchain for auditing pylint-dev/pylint under certify --repo --jail container.
# The tree is bound in at run time; only the suite's dependencies live here,
# pinned as the project's requirements_test_min.txt pins them.
FROM python:3.14-slim@sha256:0514bc76712d64f8bb48adcb95b1ab0d88c69fee95d62fe4c18ee948bee9b1e1
RUN apt-get update && apt-get install -y --no-install-recommends git && rm -rf /var/lib/apt/lists/*
RUN pip install --no-cache-dir \
    astroid==4.3.4 dill==0.4.1 isort==9.0.2 mccabe==0.7.0 platformdirs==4.13.5 tomlkit==0.15.2 \
    typing-extensions==4.16.0 py==1.11.0 pytest==9.1.1 pytest-benchmark==5.3.0 pytest-timeout==2.4.0 \
    pytest-remaster==0.1.0 requests==2.34.2 setuptools==84.2.1 gitpython==3.2.1 pytest-cov==7.1.0
