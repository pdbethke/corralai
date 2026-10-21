# Test toolchain for auditing pylint-dev/pylint under certify --repo --jail container.
# The tree is bound in at run time; only the suite's dependencies live here,
# pinned as the project's requirements_test_min.txt pins them.
FROM python:3.14-slim
RUN apt-get update && apt-get install -y --no-install-recommends git && rm -rf /var/lib/apt/lists/*
RUN pip install --no-cache-dir \
    "astroid==4.3.4" "dill>=0.3.7" "isort>=5,!=5.13,<10" "mccabe>=0.6,<0.8" "platformdirs>=2.2" "tomlkit>=0.10.1" \
    "typing-extensions~=4.15" "py~=1.11.0" "pytest>=8.4,<10.0" "pytest-benchmark~=5.1" "pytest-timeout~=2.4" \
    "pytest-remaster~=0.1.0" requests setuptools "gitpython>3" pytest-cov
