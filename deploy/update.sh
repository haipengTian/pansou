#!/usr/bin/env bash
set -Eeuo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")"

if [[ ! -f .env ]]; then
  echo "错误：缺少 deploy/.env，请先执行：cp .env.example .env" >&2
  exit 1
fi

if ! command -v docker >/dev/null 2>&1; then
  echo "错误：未找到 docker 命令" >&2
  exit 1
fi

# config 同时校验 Compose 语法和必填环境变量。
docker compose config --quiet
docker compose pull
docker compose up -d --remove-orphans

container_id="$(docker compose ps -q pansou)"
if [[ -z "$container_id" ]]; then
  echo "错误：pansou 容器未创建" >&2
  exit 1
fi

printf '等待健康检查'
for _ in {1..40}; do
  status="$(docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' "$container_id")"
  if [[ "$status" == "healthy" ]]; then
    printf '\n部署完成，容器状态：healthy\n'
    docker compose ps
    exit 0
  fi
  if [[ "$status" == "exited" || "$status" == "dead" ]]; then
    break
  fi
  printf '.'
  sleep 3
done

printf '\n错误：容器未通过健康检查，最近日志如下：\n' >&2
docker compose logs --tail=100 pansou >&2
exit 1
