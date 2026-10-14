# впн от пятерочки

Минималистичный Telegram VPN-магазин на Go поверх Partner API «Мастерская VPN».

## Возможности

- Telegram long polling; входящий порт для бота не нужен.
- Telegram Stars: invoice, pre-checkout validation, validation of successful payment currency/user/payload/amount.
- TON and USDT on TON: order-specific memo, exact expected amount, wallet/asset matching, chain event polling via TONAPI, duplicate transaction protection.
- Partner API: quote before fulfillment, `max_charge_minor`, stable `Idempotency-Key`, persistent paid-order queue and retries after restart.
- SQLite WAL database, Docker Compose, in-message admin dashboard, paginated user management, balance adjustments, bot-account blocking, provider-side VPN pause/resume, live retail-price editing, recent-order list, exchange-rate status and channel subscription controls.
- One persistent bot message per private chat: inline-button navigation edits the existing message; invoice links open Telegram Stars checkout without sending a separate invoice message. Incoming text and Telegram payment service messages are deleted where Telegram allows it.
- Referral links with first-purchase rewards credited to the internal RUB balance.
- Mandatory public-channel subscription checks with admin-managed channel list.
- Automatic TON/RUB and USDT/RUB exchange rates via CoinGecko, refreshed every 10 minutes; crypto checkout is hidden until both rates are available.
- No Platega.io integration yet.

## Before first launch

This repository is production-oriented, but **do not enable live crypto payments until you have verified the TONAPI response shape and both payment assets on mainnet using tiny test transfers**. Blockchain indexers may lag, rate-limit, or change response shapes. The watcher fails closed (a transfer that cannot be parsed/matched is not credited), but no software can guarantee a commercial service is production-ready without testing against your own provider accounts, network conditions, and payment lifecycle. Keep a reconciliation process and monitor logs.

The TONAPI Events documentation warns that high-level event action structures can change and are primarily intended for display. For a high-volume payment service, replace or supplement polling with a provider-supported payment/webhook or raw transaction verification flow and perform independent reconciliation. Do not advertise crypto as available until verified end-to-end.

## Configuration

1. Create a bot with @BotFather and set `TELEGRAM_BOT_TOKEN`.
2. Set `ADMIN_TELEGRAM_ID` to your numeric Telegram ID.
3. Create a Partner API project and set `VPN_API_KEY` and a real `VPN_PLAN_ID` from `GET /api/v1/partner/plans`.
4. Set retail RUB prices in `PRICE_7_RUB`, `PRICE_30_RUB`, `PRICE_90_RUB`; TON and USDT amounts are calculated from the latest CoinGecko rates. Stars prices are in `starsPrice()`; decide your retail prices and margin before launch.
5. Configure `TONAPI_KEY`, `TON_WALLET_ADDRESS`, and the verified mainnet USDT jetton master. Set `REFERRAL_BONUS_PERCENT` to a value from 1 to 50 (default 10). The wallet address must match the representation returned by TONAPI; raw `workchain:hex` is recommended.
6. Keep `.env` private. Do not commit it to Git.

## Run with Docker

```sh
cp .env.example .env
nano .env
docker compose up -d --build
docker compose logs -f bot
```

Data is stored in `./data/data.sqlite`. Back it up consistently (stop the service or use SQLite's backup API), and test restoring backups. Do not expose the database or API keys publicly.

## Interface

The user interface is button-driven. `/start` is only the entry point; the bot does not expose a command menu. Navigation edits one stored message per private chat. The admin panel is available as a button only to `ADMIN_TELEGRAM_ID`. It includes users, balances, account blocking, provider VPN pause/resume, exchange-rate status, and required-channel management. To add a channel, the admin sends its public `@username` when prompted; the bot must be an administrator in each required channel.

## Prices and commercial settings

- Stars retail prices are in `starsPrice()` (`main.go`). Telegram Stars are the payment currency for digital goods sold inside Telegram.
- TON and USDT prices are calculated from current RUB exchange rates and rounded to nanoTON / micro-USDT. The last stored rate is reused during brief provider outages; crypto checkout is hidden when the last rate is older than two hours.
- Partner API charges your provider wallet, not the buyer. Keep sufficient provider balance. The bot requests a quote and sends `max_charge_minor` to cap the provider-side charge.

## Operational checklist

- Test Stars purchase, cancellation, duplicate updates, successful payment and refund workflow.
- Test TON and USDT transfers with exact memo and wrong memo, wrong amount, wrong recipient, wrong USDT jetton, expired order and duplicate scan.
- Confirm the provider's API key is live (not test) before serving real customers.
- Configure process monitoring, alerting, backup retention, restore drills, support contact, terms, privacy notice, refund policy and applicable tax/legal requirements.
- Monitor Partner API balance and failed orders. A confirmed payment with failed fulfillment remains in the `paid` queue and is retried using the same idempotency key.
- Review Telegram's current Stars terms and the TONAPI provider's limits before launch.

## Documentation

- Partner API: https://api.subkeyapi.com/api/v1/partner/docs
- TONAPI events: https://docs.tonconsole.com/tonapi/rest-api/events
