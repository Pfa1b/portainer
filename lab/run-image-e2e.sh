#!/usr/bin/env bash
# Only disposable, uniquely named local containers. Never mount a host Docker socket.
set -euo pipefail
image=${1:-portainer:2.39.1-bayai-fix}
root=$(cd "$(dirname "$0")/.." && pwd)
results=${LAB_RESULTS:-"$root/lab-results"}
mkdir -p "$results"
for name in astra-portainer-fixed astra-portainer-dind astra-ecr-fake astra-portainer-client; do
  if docker container inspect "$name" >/dev/null 2>&1; then
    echo "Refusing to reuse existing lab container: $name" >&2; exit 2
  fi
done
if docker network inspect astra-portainer-lab >/dev/null 2>&1; then
  echo 'Refusing to reuse existing lab network' >&2; exit 2
fi
scratch=$(mktemp -d "$root/.astra-lab.XXXXXX")
cleanup() {
  docker logs astra-ecr-fake > "$results/fake-ecr.log" 2>&1 || true
  docker logs astra-portainer-fixed 2>&1 | grep -E 'ECR token refresh|ECR registry persistence|compose deployment|stack update transaction' > "$results/stages.log" || true
  docker rm -fv astra-portainer-client astra-portainer-fixed astra-ecr-fake astra-portainer-dind >/dev/null 2>&1 || true
  docker network rm astra-portainer-lab >/dev/null 2>&1 || true
  rm -rf "$scratch"
}
trap cleanup EXIT
openssl req -x509 -newkey rsa:2048 -nodes -keyout "$scratch/key.pem" -out "$scratch/ca.pem" -days 2 -subj '/CN=lab-ecr' -addext 'subjectAltName=DNS:api.ecr.us-east-1.amazonaws.com' >/dev/null 2>&1
cp "$root/lab/fake_ecr.py" "$root/lab/image_e2e.py" "$scratch/"
touch "$scratch/expired"
docker pull alpine:3.20 >/dev/null
docker pull python:3.11-slim-bookworm >/dev/null
docker pull docker:28-dind >/dev/null
docker network create --internal astra-portainer-lab >/dev/null
docker run -d --privileged --name astra-portainer-dind --network astra-portainer-lab -e DOCKER_TLS_CERTDIR= docker:28-dind --tls=false >/dev/null
for attempt in $(seq 1 30); do
  if docker exec astra-portainer-dind docker info >/dev/null 2>&1; then break; fi
  sleep 1
done
docker save alpine:3.20 | docker exec -i astra-portainer-dind docker load >/dev/null
docker run -d --name astra-ecr-fake --network astra-portainer-lab -v "$scratch:/lab:ro" python:3.11-slim-bookworm python -B /lab/fake_ecr.py >/dev/null
docker run -d --name astra-portainer-fixed --network astra-portainer-lab -e HTTPS_PROXY=http://astra-ecr-fake:8080 -e NO_PROXY=localhost,127.0.0.1,astra-portainer-dind -e SSL_CERT_FILE=/lab/ca.pem -v "$scratch/ca.pem:/lab/ca.pem:ro" "$image" --http-enabled --log-level DEBUG >/dev/null
docker exec astra-ecr-fake python -B -c '
import time,urllib.request
for attempt in range(30):
 try:
  urllib.request.urlopen("http://astra-portainer-fixed:9000/api/status",timeout=1);break
 except OSError:time.sleep(1)
else:raise SystemExit("Portainer lab did not start")
'
docker run --rm --name astra-portainer-client --network astra-portainer-lab -v "$scratch:/lab" python:3.11-slim-bookworm python -B /lab/image_e2e.py | tee "$results/image-e2e.json"
docker image inspect "$image" --format '{{.Id}} {{.Architecture}} {{json .RepoDigests}}' > "$results/image-identity.txt"
