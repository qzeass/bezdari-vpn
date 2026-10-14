# Deploy on Linux

1. Install Docker Engine and Docker Compose plugin.
2. Upload and unpack this folder on the server.
3. Create `.env` from `.env.example`, then fill all secrets and real provider identifiers.
4. Check the Partner API key and plan in the provider dashboard. Use a test key and test orders first.
5. Build and start: `docker compose up -d --build`.
6. Inspect logs: `docker compose logs -f bot`.
7. Configure server-level monitoring for container restarts and disk space. Keep `/data` on persistent storage and back it up off-server.

The bot uses Telegram long polling, so no inbound port or reverse proxy is required for its current features. `bezdari.name` can point to a future web app, but the domain alone does not make the bot publicly reachable or configure Telegram payments.

## Before live payments

- Make a low-value TON test transfer with the exact memo and verify the bot matches recipient, amount, and event.
- Repeat for USDT using the exact mainnet USDT jetton master address.
- Try wrong amount, wrong memo, wrong recipient, and duplicate event; none must grant access.
- Confirm a paid order is retried after stopping the container between payment and fulfillment.
- Verify Partner API balance, plan, and live key.
- Review Stars terms and implement/operate a support-led refund procedure.
