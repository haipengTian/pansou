# PanSou 服务器部署

该目录用于部署本 Fork 的 `production` 镜像。默认拓扑为：

```text
公网 HTTPS -> Nginx/Caddy -> 127.0.0.1:8888 -> PanSou
```

## 1. GitHub 侧准备

1. 仓库 `Settings -> Actions -> General -> Workflow permissions` 选择 **Read and write permissions**。
2. 在 Actions 页面启用 Fork 的工作流。
3. 将验证完成的 `develop` 合并到 `production` 并推送。工作流会发布：
   - `ghcr.io/haipengtian/pansou:production`
   - `ghcr.io/haipengtian/pansou:latest`
   - `ghcr.io/haipengtian/pansou:sha-<提交短哈希>`
4. 在 GitHub Packages 中将镜像设为 Public；如果保持 Private，请在服务器登录 GHCR：

```bash
echo '<具有 read:packages 权限的 PAT>' | docker login ghcr.io -u haipengTian --password-stdin
```

## 2. 首次部署

服务器需安装 Docker Engine 和 Docker Compose v2。将本目录复制或克隆到服务器后执行：

```bash
cd /opt/pansou/deploy
cp .env.example .env
openssl rand -hex 32
```

编辑 `.env`：

- 将 `AUTH_USERS` 设置为 `用户名:强密码`。
- 将 `AUTH_JWT_SECRET` 设置为刚生成的随机字符串。
- 按需调整 `CHANNELS`、`ENABLED_PLUGINS` 和代理。

然后启动：

```bash
chmod 600 .env
chmod +x update.sh
./update.sh
curl http://127.0.0.1:8888/api/health
```

也可以手动执行：

```bash
docker compose config
docker compose pull
docker compose up -d --remove-orphans
docker compose logs -f --tail=200
```

## 3. 域名与 HTTPS

复制 `nginx.conf.example` 到 Nginx 配置目录并替换域名，检查并重载：

```bash
sudo nginx -t
sudo systemctl reload nginx
```

随后使用 Certbot 或现有网关签发 HTTPS 证书。启用认证时也应使用 HTTPS，避免登录凭据明文传输。

如果明确需要通过 IP 直接访问，可将 `.env` 中的 `PANSOU_BIND_ADDRESS` 改为 `0.0.0.0`，并开放防火墙端口；公网环境不推荐这样做。

## 4. 更新

Actions 构建完成后在服务器执行：

```bash
cd /opt/pansou/deploy
./update.sh
```

命名卷 `pansou-cache` 会在更新容器时保留缓存。

## 5. 回滚

在 GitHub Packages 或 Actions 日志中找到上一个 SHA 标签，将 `.env` 改为：

```dotenv
PANSOU_IMAGE=ghcr.io/haipengtian/pansou:sha-a1b2c3d
```

然后执行 `./update.sh`。确认修复后再切回 `:production`。

## 6. 常用命令

```bash
# 状态与日志
docker compose ps
docker compose logs -f --tail=200

# 重启
docker compose restart pansou

# 停止（保留缓存卷）
docker compose down

# 删除服务及缓存卷（会丢失缓存，谨慎执行）
docker compose down -v
```
