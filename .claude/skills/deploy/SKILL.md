---
name: deploy
description: Build and deploy audiobook-organizer to the production Linux server
disable-model-invocation: true
---

# Deploy to Production

Deploy the audiobook-organizer to the production Linux server. Always use `make deploy` — never manual scp/ssh.

## Pre-flight Checks

1. Ensure you're on `main` branch and it's clean:
   ```bash
   git status
   git branch --show-current
   ```

2. Run tests to verify nothing is broken:
   ```bash
   make test
   ```

3. Verify the frontend builds:
   ```bash
   make build
   ```

## Deploy

```bash
make deploy
```

This builds the Linux binary with embedded frontend and deploys to the production server at <server-ip>.

## Post-deploy Verification

After deploy completes, verify the service is running:
```bash
ssh <user>@<server-ip> 'curl -sk https://localhost:8484/api/v1/health'
```

## Rules

- **NEVER** deploy from a feature branch
- **NEVER** deploy without running tests first
- **NEVER** use manual scp/ssh to copy binaries — always `make deploy`
- If tests fail, fix them before deploying
