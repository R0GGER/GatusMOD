# GatusMOD

> **Early release.** GatusMOD is still in early development. Expect bugs, rough edges, and breaking changes to the configuration between versions. Test it before relying on it in production, keep backups of your configuration and data, and please report any problems you run into.

<p align="center">
  <img src="config/favicon/gatusmod_128.png" alt="GatusMOD logo" width="128" height="128">
</p>

GatusMOD is an overlay for [Gatus](https://github.com/TwiN/gatus). It runs as a separate container in front of Gatus and adds features without changing the Gatus code.

- **Central database**: manage endpoints in NocoDB or Baserow. Per database several tables or views (for example `NocoDB-prod:http-sites` and `NocoDB-prod:iperf3-servers`), each with its own primary check, field mapping, and endpoint templates, become their own Gatus YAML file that Gatus reloads automatically.
- **Protocols Gatus cannot check**: iperf3 (handshake and an optional bandwidth test), NTP, SNMP, SMTP, PostgreSQL, MySQL/MariaDB, Redis, MongoDB, and **push** endpoints (a script reports its status to a URL). GatusMOD runs these checks and reports the results to Gatus as external endpoints, so the dashboard, uptime, badges, and alerts keep working.
- **Schedule per table/view** (aligned to the clock, a custom cron expression, or manual only) and a default check interval (`defaults.interval`).
- **Pagination**: number of endpoints per dashboard page (Gatus is fixed at 96).
- **Favicons**: `default`, `16x16`, `32x32`, `apple-touch`, `192x192`, `512x512` (also in `manifest.json`), with a `[baseurl]` placeholder.
- **Footer** HTML that replaces Gatus's "Powered by Gatus" line, **hide the heading and subtitle**, a **12/24-hour clock and DD/MM or MM/DD date notation**, and **analytics** (Umami or Plausible).
- **Admin page** (`/mod/admin`, session login): databases and their tables/views, general settings, password change, sync status, import of an existing Gatus YAML file, and a **badge generator**.
- **Public uptime badge** across all endpoints, a group, or a table/view (`/mod/badges/...`).
- **Status pages** per group and per endpoint: the endpoints of one or more groups, plus single endpoints, with their own title and heading, at `/status/<slug>` and on their own custom domains. **Private status pages** need a shared password, and their endpoints are hidden everywhere else.



## Quick start

1. Copy `docker-compose.yml` and the `config/` directory to your server. The example `config/config.yaml` already contains `skip-invalid-config-update: true` (so an invalid generated file does not crash Gatus) and a `gatusmod:` block (see [Configuration](docs.md#configuration)).
2. Start with `docker compose up -d`. The Compose file looks like this:
  ```bash
   services:
    gatus:
      image: ghcr.io/twin/gatus:latest
      restart: unless-stopped
      environment:
        GATUS_CONFIG_PATH: /config
      volumes:
        - ./config:/config
        - ./config/db:/data
    # No published port: Gatus is only reachable through GatusMOD.
    gatusmod:
      image: ghcr.io/r0gger/gatusmod:latest
      restart: unless-stopped
      depends_on:
        - gatus
      ports:
        - "8080:8080"
      environment:
        GATUSMOD_CONFIG_PATH: /config
        GATUSMOD_DATA_PATH: /config      
      volumes:
        - ./config:/config
        - ./config/favicon:/favicon:ro
        - ./config/public:/public:ro
  ```
3. Login at [http://localhost:8080/mod/admin](http://localhost:8080/mod/admin) with **username:** `admin` and **password:** `GatusMOD`.
4. Change the default password as soon as possible. Generate a bcrypt hash of your new password:
   ```bash
   docker compose run --rm gatusmod hash-password 'your-new-password'
   ```
   Replace the value of `gatusmod.admin.password-bcrypt` in `config/config.yaml` with the output, exactly as printed. The hash already contains doubled `$$`, because Gatus and GatusMOD treat `$VAR` as an environment variable. GatusMOD picks up the change within 30 seconds.


## Adding GatusMOD to an existing Gatus setup? 

Point Gatus at the config **directory** (`GATUS_CONFIG_PATH=/config`), mount that same directory in GatusMOD, and add `skip-invalid-config-update: true` to your Gatus config.
Gatus will not start if there is no endpoint anywhere. Keep at least one endpoint in `config.yaml`, or start GatusMOD first so the generated file already exists.

## More docs

Configuration, databases, the extra protocols, the admin page, badges, status pages, the statistics table, and development are documented in [docs.md](docs.md).

## License

GatusMOD is an independent, third-party overlay and is not affiliated with, maintained by, or endorsed by the official Gatus project.
