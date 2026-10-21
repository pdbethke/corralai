# Test toolchain for auditing Kludex/starlette under certify --repo --jail container.
# The tree is bound in at run time; only the suite's dependencies live here.
FROM python:3.14-slim
RUN pip install --no-cache-dir \
    "anyio>=4,<5" itsdangerous jinja2 opentelemetry-api opentelemetry-sdk "python-multipart>=0.0.18" pyyaml \
    "httpx>=0.27.0,<0.29.0" "httpx2[zstd]>=2.0.0" trio blockbuster importlib-metadata \
    "pytest>=9.1.1" pytest-cov
