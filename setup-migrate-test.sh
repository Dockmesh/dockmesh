#!/bin/bash
set -e
rm -rf /tmp/migrate-test
mkdir -p /tmp/migrate-test
mkdir -p /tmp/migrate-test/nginx-web
mkdir -p /tmp/migrate-test/my_awesome_app
mkdir -p "/tmp/migrate-test/  space name  "
mkdir -p /tmp/migrate-test/admin
mkdir -p /tmp/migrate-test/redis-cache
mkdir -p /tmp/migrate-test/not-a-stack
cat > /tmp/migrate-test/nginx-web/compose.yaml <<'EOF'
services:
  web:
    image: nginx:alpine
    ports:
      - "8080:80"
EOF
cat > /tmp/migrate-test/my_awesome_app/docker-compose.yml <<'EOF'
services:
  app:
    image: busybox
    command: sleep infinity
EOF
echo "APP_KEY=secret123" > /tmp/migrate-test/my_awesome_app/.env
cat > "/tmp/migrate-test/  space name  /compose.yml" <<'EOF'
services:
  demo:
    image: busybox
EOF
cat > /tmp/migrate-test/admin/compose.yaml <<'EOF'
services:
  bad:
    image: busybox
EOF
cat > /tmp/migrate-test/redis-cache/compose.yaml <<'EOF'
services:
  redis:
    image: redis:7-alpine
EOF
echo "just a note" > /tmp/migrate-test/not-a-stack/readme.txt
ls /tmp/migrate-test/
