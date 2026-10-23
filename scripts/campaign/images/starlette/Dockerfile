# Test toolchain for auditing Kludex/starlette under certify --repo --jail container.
# The tree is bound in at run time; only the suite's dependencies live here.
FROM python:3.14-slim@sha256:0514bc76712d64f8bb48adcb95b1ab0d88c69fee95d62fe4c18ee948bee9b1e1
RUN pip install --no-cache-dir \
    anyio==4.15.1 itsdangerous==2.2.0 jinja2==3.1.6 opentelemetry-api==1.45.1 opentelemetry-sdk==1.45.1 python-multipart==0.0.32 pyyaml==6.0.3 \
    httpx==0.28.1 "httpx2[zstd]==2.16.0" trio==0.34.0 blockbuster==1.5.29 importlib-metadata==9.1.0 \
    pytest==9.1.1 pytest-cov==7.1.0
