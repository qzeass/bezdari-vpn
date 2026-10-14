package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

type Config struct {
	BotToken, AdminID, APIBase, APIKey, PlanID, PublicURL, TONWallet, TONAPIKey, USDTMaster, PaymentSecret, Support string
	TONPrice7, TONPrice30, TONPrice90, USDTPrice7, USDTPrice30, USDTPrice90                                         string
	TONAPIBase                                                                                                      string
	RubPrice7, RubPrice30, RubPrice90, ReferralBonusPercent                                                         string
}
type App struct {
	cfg         Config
	db          *sql.DB
	client      *http.Client
	rateMu      sync.RWMutex
	tonRub      float64
	usdtRub     float64
	rateUpdated time.Time
}
type tgUpdate struct {
	UpdateID    int            `json:"update_id"`
	Message     *tgMessage     `json:"message"`
	Callback    *tgCallback    `json:"callback_query"`
	PreCheckout *tgPreCheckout `json:"pre_checkout_query"`
}
type tgCallback struct {
	ID   string `json:"id"`
	Data string `json:"data"`
	From struct {
		ID int64 `json:"id"`
	} `json:"from"`
	Message *tgMessage `json:"message"`
}
type tgMessage struct {
	MessageID int `json:"message_id"`
	Chat      struct {
		ID int64 `json:"id"`
	} `json:"chat"`
	From struct {
		ID       int64  `json:"id"`
		Username string `json:"username"`
	} `json:"from"`
	Text              string `json:"text"`
	SuccessfulPayment *struct {
		Currency string `json:"currency"`
		Total    int    `json:"total_amount"`
		Payload  string `json:"invoice_payload"`
		ChargeID string `json:"telegram_payment_charge_id"`
	} `json:"successful_payment"`
}
type tgPreCheckout struct {
	ID   string `json:"id"`
	From struct {
		ID int64 `json:"id"`
	} `json:"from"`
	Currency string `json:"currency"`
	Total    int    `json:"total_amount"`
	Payload  string `json:"invoice_payload"`
}
type tgResp struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	Description string          `json:"description"`
}
type plan struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Kind          string `json:"kind"`
	DurationDays  int    `json:"duration_days"`
	DeviceLimit   int    `json:"device_limit"`
	PriceMinor    string `json:"price_minor"`
	Currency      string `json:"currency"`
	OrdersEnabled bool   `json:"orders_enabled"`
}
type partnerUser struct {
	OrderID         string `json:"order_id"`
	Status          string `json:"status"`
	ExpiresAt       string `json:"expires_at"`
	SubscriptionURL string `json:"subscription_url"`
	ChargedMinor    string `json:"charged_minor"`
}

func env(k string) string { return strings.TrimSpace(os.Getenv(k)) }
func main() {
	cfg := Config{BotToken: env("TELEGRAM_BOT_TOKEN"), AdminID: env("ADMIN_TELEGRAM_ID"), APIBase: strings.TrimRight(envDefault("VPN_API_BASE", "https://api.subkeyapi.com"), "/"), APIKey: env("VPN_API_KEY"), PlanID: env("VPN_PLAN_ID"), PublicURL: env("PUBLIC_URL"), TONWallet: env("TON_WALLET_ADDRESS"), TONAPIKey: env("TONAPI_KEY"), USDTMaster: env("USDT_JETTON_MASTER"), PaymentSecret: env("PAYMENT_SECRET"), Support: envDefault("SUPPORT_USERNAME", ""), TONPrice7: env("TON_PRICE_7_NANO"), TONPrice30: env("TON_PRICE_30_NANO"), TONPrice90: env("TON_PRICE_90_NANO"), USDTPrice7: env("USDT_PRICE_7_MICRO"), USDTPrice30: env("USDT_PRICE_30_MICRO"), USDTPrice90: env("USDT_PRICE_90_MICRO"), TONAPIBase: strings.TrimRight(envDefault("TONAPI_BASE", "https://tonapi.io"), "/"), RubPrice7: envDefault("PRICE_7_RUB", "49"), RubPrice30: envDefault("PRICE_30_RUB", "149"), RubPrice90: envDefault("PRICE_90_RUB", "349"), ReferralBonusPercent: envDefault("REFERRAL_BONUS_PERCENT", "10")}
	if cfg.BotToken == "" || cfg.APIKey == "" || cfg.PlanID == "" {
		log.Fatal("Set TELEGRAM_BOT_TOKEN, VPN_API_KEY and VPN_PLAN_ID in .env")
	}
	db, err := sql.Open("sqlite", envDefault("DATABASE_PATH", "./data.sqlite"))
	if err != nil {
		log.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	a := &App{cfg: cfg, db: db, client: &http.Client{Timeout: 20 * time.Second}}
	if err = a.initDB(); err != nil {
		log.Fatal(err)
	}
	if err = a.checkPartner(); err != nil {
		log.Fatalf("Partner API check failed: %v", err)
	}
	_, _ = a.db.Exec(`CREATE TABLE IF NOT EXISTS ui_messages(chat_id INTEGER PRIMARY KEY, message_id INTEGER NOT NULL, updated_at INTEGER NOT NULL)`)
	_ = a.tg("setMyCommands", map[string]any{"commands": []any{}}, nil)
	log.Println("впн от пятерочки: bot started")
	if a.cryptoEnabled() {
		go a.tonPaymentWatcher()
	}
	go a.fulfillmentWorker()
	go a.exchangeRateWorker()
	a.pollTelegram()
}
func envDefault(k, d string) string {
	if v := env(k); v != "" {
		return v
	}
	return d
}
func (a *App) initDB() error {
	_, e := a.db.Exec(`PRAGMA journal_mode=WAL; CREATE TABLE IF NOT EXISTS users(tg_id INTEGER PRIMARY KEY, username TEXT, created_at INTEGER NOT NULL); CREATE TABLE IF NOT EXISTS ui_messages(chat_id INTEGER PRIMARY KEY, message_id INTEGER NOT NULL, updated_at INTEGER NOT NULL); CREATE TABLE IF NOT EXISTS orders(id TEXT PRIMARY KEY,tg_id INTEGER NOT NULL,days INTEGER NOT NULL,method TEXT NOT NULL,status TEXT NOT NULL,expected_amount TEXT NOT NULL DEFAULT '',currency TEXT NOT NULL DEFAULT '',tx_hash TEXT NOT NULL DEFAULT '',created_at INTEGER NOT NULL,updated_at INTEGER NOT NULL,subscription_url TEXT NOT NULL DEFAULT '',expires_at TEXT NOT NULL DEFAULT '',partner_order_id TEXT NOT NULL DEFAULT ''); CREATE UNIQUE INDEX IF NOT EXISTS orders_tx ON orders(tx_hash) WHERE tx_hash<>''; CREATE TABLE IF NOT EXISTS processed_updates(update_id INTEGER PRIMARY KEY);
 CREATE TABLE IF NOT EXISTS user_meta(tg_id INTEGER PRIMARY KEY, balance_rub INTEGER NOT NULL DEFAULT 0, blocked INTEGER NOT NULL DEFAULT 0, vpn_paused INTEGER NOT NULL DEFAULT 0, referrer_id INTEGER NOT NULL DEFAULT 0, referral_rewarded INTEGER NOT NULL DEFAULT 0, created_at INTEGER NOT NULL DEFAULT 0);
 CREATE TABLE IF NOT EXISTS required_channels(chat_id TEXT PRIMARY KEY, title TEXT NOT NULL DEFAULT '', added_at INTEGER NOT NULL);
 CREATE TABLE IF NOT EXISTS admin_inputs(tg_id INTEGER PRIMARY KEY, action TEXT NOT NULL, payload TEXT NOT NULL DEFAULT '', created_at INTEGER NOT NULL);
 CREATE TABLE IF NOT EXISTS app_settings(key TEXT PRIMARY KEY, value TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS exchange_rates(asset TEXT PRIMARY KEY, rub REAL NOT NULL, updated_at INTEGER NOT NULL);`)
	if e != nil {
		return e
	}
	// Backward-compatible migration for existing installs.
	_, _ = a.db.Exec(`ALTER TABLE orders ADD COLUMN payment_memo TEXT NOT NULL DEFAULT ''`)
	_, _ = a.db.Exec(`ALTER TABLE orders ADD COLUMN notified_at INTEGER NOT NULL DEFAULT 0`)
	_, _ = a.db.Exec(`ALTER TABLE user_meta ADD COLUMN vpn_paused INTEGER NOT NULL DEFAULT 0`)
	return nil
}
func (a *App) partner(path, method string, body any, key string, out any) error {
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, e := http.NewRequest(method, a.cfg.APIBase+"/api/v1/partner"+path, r)
	if e != nil {
		return e
	}
	req.Header.Set("Authorization", "Bearer "+a.cfg.APIKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	resp, e := a.client.Do(req)
	if e != nil {
		return e
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("partner API HTTP %d: %s", resp.StatusCode, string(b))
	}
	if out != nil {
		return json.Unmarshal(b, out)
	}
	return nil
}
func (a *App) checkPartner() error {
	var v struct {
		Mode  string `json:"mode"`
		Brand string `json:"brand"`
	}
	if e := a.partner("/me", "GET", nil, "", &v); e != nil {
		return e
	}
	if strings.HasPrefix(a.cfg.APIKey, "uk_test_") {
		log.Printf("WARNING: Partner API is in test mode")
	}
	log.Printf("Partner API connected: %s (mode=%s)", v.Brand, v.Mode)
	return nil
}
func (a *App) pollTelegram() {
	offset := 0
	for {
		var updates []tgUpdate
		e := a.tg("getUpdates", map[string]any{"offset": offset, "timeout": 45, "allowed_updates": []string{"message", "callback_query", "pre_checkout_query"}}, &updates)
		if e != nil {
			log.Printf("telegram polling: %v", e)
			time.Sleep(3 * time.Second)
			continue
		}
		for _, u := range updates {
			offset = u.UpdateID + 1
			if u.PreCheckout != nil {
				a.preCheckout(*u.PreCheckout)
			}
			if u.Message != nil {
				a.handleMessage(*u.Message)
			}
			if u.Callback != nil {
				a.handleCallback(*u.Callback)
			}
		}
	}
}
func (a *App) tg(method string, payload any, out any) error {
	b, _ := json.Marshal(payload)
	req, e := http.NewRequest("POST", "https://api.telegram.org/bot"+a.cfg.BotToken+"/"+method, bytes.NewReader(b))
	if e != nil {
		return e
	}
	req.Header.Set("Content-Type", "application/json")
	r, e := a.client.Do(req)
	if e != nil {
		return e
	}
	defer r.Body.Close()
	var tr tgResp
	if e = json.NewDecoder(r.Body).Decode(&tr); e != nil {
		return e
	}
	if !tr.OK {
		return errors.New(tr.Description)
	}
	if out != nil {
		return json.Unmarshal(tr.Result, out)
	}
	return nil
}
func (a *App) send(chat int64, text string, markup any) {
	// The bot maintains one persistent UI message per private chat. Every navigation
	// action edits that message instead of sending another menu message.
	var messageID int
	err := a.db.QueryRow(`SELECT message_id FROM ui_messages WHERE chat_id=?`, chat).Scan(&messageID)
	if err == nil && messageID > 0 {
		payload := map[string]any{"chat_id": chat, "message_id": messageID, "text": text, "parse_mode": "HTML", "disable_web_page_preview": true}
		if markup != nil {
			payload["reply_markup"] = markup
		} else {
			payload["reply_markup"] = map[string]any{"inline_keyboard": [][]any{}}
		}
		e := a.tg("editMessageText", payload, nil)
		if e == nil || strings.Contains(strings.ToLower(e.Error()), "message is not modified") {
			return
		}
		log.Printf("edit UI chat=%d: %v; sending replacement", chat, e)
	}
	payload := map[string]any{"chat_id": chat, "text": text, "parse_mode": "HTML", "disable_web_page_preview": true}
	if markup != nil {
		payload["reply_markup"] = markup
	}
	var sent struct {
		MessageID int `json:"message_id"`
	}
	if e := a.tg("sendMessage", payload, &sent); e != nil {
		log.Printf("send UI chat=%d: %v", chat, e)
		return
	}
	if sent.MessageID > 0 {
		if messageID > 0 && messageID != sent.MessageID {
			// If Telegram made the old message uneditable, replace it and remove the old one.
			_ = a.tg("deleteMessage", map[string]any{"chat_id": chat, "message_id": messageID}, nil)
		}
		_, _ = a.db.Exec(`INSERT INTO ui_messages(chat_id,message_id,updated_at) VALUES(?,?,?) ON CONFLICT(chat_id) DO UPDATE SET message_id=excluded.message_id,updated_at=excluded.updated_at`, chat, sent.MessageID, time.Now().Unix())
	}
}
func button(text, data, style string) map[string]string {
	b := map[string]string{"text": text, "callback_data": data}
	if style != "" {
		b["style"] = style
	}
	return b
}
func urlButton(text, url string, style string) map[string]string {
	b := map[string]string{"text": text, "url": url}
	if style != "" {
		b["style"] = style
	}
	return b
}
func inline(rows ...[]map[string]string) any { return map[string]any{"inline_keyboard": rows} }
func (a *App) homeKeyboard(chat int64) any {
	rows := [][]map[string]string{
		{button("Купить доступ", "buy", "primary")},
		{button("Мой VPN", "status", "success"), button("Тарифы", "buy", "")},
		{button("Баланс", "balance", ""), button("Пригласить друга", "referral", "")},
		{button("Поддержка", "help", "")},
	}
	if a.cfg.AdminID != "" && strconv.FormatInt(chat, 10) == a.cfg.AdminID {
		rows = append(rows, []map[string]string{button("Администрирование", "admin", "")})
	}
	return inline(rows...)
}
func (a *App) home(chat int64) {
	if !a.hasRequiredChannels(chat) {
		a.showRequiredChannels(chat)
		return
	}
	a.send(chat, "<b>впн от пятерочки</b>\n\nПриватный VPN-доступ без лишних настроек.\n\nВыбери действие.", a.homeKeyboard(chat))
}
func (a *App) handleMessage(m tgMessage) {
	id := m.From.ID
	_, _ = a.db.Exec(`INSERT INTO users(tg_id,username,created_at) VALUES(?,?,?) ON CONFLICT(tg_id) DO UPDATE SET username=excluded.username`, id, m.From.Username, time.Now().Unix())
	if m.Text != "" && strings.HasPrefix(m.Text, "/start") {
		parts := strings.Fields(m.Text)
		if len(parts) > 1 && strings.HasPrefix(parts[1], "ref_") {
			refID, _ := strconv.ParseInt(strings.TrimPrefix(parts[1], "ref_"), 10, 64)
			if refID > 0 && refID != id {
				_, _ = a.db.Exec(`INSERT INTO user_meta(tg_id,created_at,referrer_id) VALUES(?,?,?) ON CONFLICT(tg_id) DO UPDATE SET referrer_id=CASE WHEN user_meta.referrer_id=0 THEN excluded.referrer_id ELSE user_meta.referrer_id END`, id, time.Now().Unix(), refID)
			}
		}
	}
	_, _ = a.db.Exec(`INSERT INTO user_meta(tg_id,created_at) VALUES(?,?) ON CONFLICT(tg_id) DO NOTHING`, id, time.Now().Unix())
	if m.Text != "" && strconv.FormatInt(id, 10) == a.cfg.AdminID {
		var action, payload string
		if a.db.QueryRow(`SELECT action,payload FROM admin_inputs WHERE tg_id=?`, id).Scan(&action, &payload) == nil {
			_ = a.tg("deleteMessage", map[string]any{"chat_id": m.Chat.ID, "message_id": m.MessageID}, nil)
			a.handleAdminInput(m.Chat.ID, id, action, payload, m.Text)
			return
		}
	}
	if m.SuccessfulPayment != nil {
		// Telegram posts a service message for successful payments. Remove it so the
		// conversation remains visually centred around the single editable UI message.
		_ = a.tg("deleteMessage", map[string]any{"chat_id": m.Chat.ID, "message_id": m.MessageID}, nil)
		a.handleStars(m)
		return
	}
	var blocked int
	_ = a.db.QueryRow(`SELECT blocked FROM user_meta WHERE tg_id=?`, id).Scan(&blocked)
	if blocked == 1 && strconv.FormatInt(id, 10) != a.cfg.AdminID {
		_ = a.tg("deleteMessage", map[string]any{"chat_id": m.Chat.ID, "message_id": m.MessageID}, nil)
		return
	}
	// Commands are intentionally not part of the UX. /start is only the entry point;
	// any other text returns the user to the single-message home screen.
	if m.Text != "" {
		// Keep the chat clean: remove /start and any accidental text after handling it.
		_ = a.tg("deleteMessage", map[string]any{"chat_id": m.Chat.ID, "message_id": m.MessageID}, nil)
	}
	a.home(m.Chat.ID)
}

func (a *App) handleCallback(c tgCallback) {
	if c.Message == nil {
		return
	}
	chat, uid := c.Message.Chat.ID, c.From.ID
	if !a.isAdmin(uid) {
		var blocked int
		_ = a.db.QueryRow(`SELECT blocked FROM user_meta WHERE tg_id=?`, uid).Scan(&blocked)
		if blocked == 1 {
			_ = a.tg("answerCallbackQuery", map[string]any{"callback_query_id": c.ID, "text": "Доступ к боту отключён."}, nil)
			return
		}
	}
	if !a.isAdmin(uid) && c.Data != "channels:check" && !strings.HasPrefix(c.Data, "admin") && !a.hasRequiredChannels(uid) {
		_ = a.tg("answerCallbackQuery", map[string]any{"callback_query_id": c.ID, "text": "Сначала подпишись на обязательные каналы."}, nil)
		a.showRequiredChannels(chat)
		return
	}
	_ = a.tg("answerCallbackQuery", map[string]any{"callback_query_id": c.ID}, nil)
	switch {
	case c.Data == "home":
		a.home(chat)
	case c.Data == "buy":
		a.showBuy(chat)
	case c.Data == "status":
		a.showStatus(chat, uid)
	case c.Data == "help":
		a.showHelp(chat)
	case c.Data == "admin":
		a.showAdmin(chat, uid)
	case c.Data == "admin:users":
		a.showAdminUsers(chat, uid, 0)
	case strings.HasPrefix(c.Data, "admin:users:"):
		page, _ := strconv.Atoi(strings.TrimPrefix(c.Data, "admin:users:"))
		a.showAdminUsers(chat, uid, page)
	case strings.HasPrefix(c.Data, "admin:user:"):
		target, _ := strconv.ParseInt(strings.TrimPrefix(c.Data, "admin:user:"), 10, 64)
		a.showAdminUser(chat, uid, target)
	case strings.HasPrefix(c.Data, "admin:bal:"):
		parts := strings.Split(c.Data, ":")
		if len(parts) == 5 {
			target, _ := strconv.ParseInt(parts[3], 10, 64)
			delta, _ := strconv.ParseInt(parts[4], 10, 64)
			if parts[2] == "sub" {
				delta = -delta
			}
			a.adjustBalance(chat, uid, target, delta)
		}
	case strings.HasPrefix(c.Data, "admin:block:"):
		target, _ := strconv.ParseInt(strings.TrimPrefix(c.Data, "admin:block:"), 10, 64)
		a.toggleBlock(chat, uid, target)
	case strings.HasPrefix(c.Data, "admin:vpn:disable:"):
		target, _ := strconv.ParseInt(strings.TrimPrefix(c.Data, "admin:vpn:disable:"), 10, 64)
		a.setVPNPaused(chat, uid, target, true)
	case strings.HasPrefix(c.Data, "admin:vpn:enable:"):
		target, _ := strconv.ParseInt(strings.TrimPrefix(c.Data, "admin:vpn:enable:"), 10, 64)
		a.setVPNPaused(chat, uid, target, false)
	case c.Data == "admin:channels":
		a.showAdminChannels(chat, uid)
	case c.Data == "admin:channel:add":
		a.askAdminInput(chat, uid, "channel_add", "")
	case strings.HasPrefix(c.Data, "admin:channel:del:"):
		a.deleteChannel(chat, uid, strings.TrimPrefix(c.Data, "admin:channel:del:"))
	case c.Data == "admin:rates":
		a.showRates(chat, uid)
	case c.Data == "admin:prices":
		a.showAdminPrices(chat, uid)
	case c.Data == "admin:orders":
		a.showAdminOrders(chat, uid)
	case strings.HasPrefix(c.Data, "admin:price:set:"):
		days, _ := strconv.Atoi(strings.TrimPrefix(c.Data, "admin:price:set:"))
		if validDays(days) {
			a.askAdminInput(chat, uid, "price_set", strconv.Itoa(days))
		}
	case c.Data == "referral":
		a.showReferral(chat, uid)
	case c.Data == "balance":
		a.showBalance(chat, uid)
	case strings.HasPrefix(c.Data, "paybalance:"):
		days, _ := strconv.Atoi(strings.TrimPrefix(c.Data, "paybalance:"))
		if validDays(days) {
			a.payWithBalance(chat, uid, days)
		}
	case c.Data == "channels:check":
		a.checkRequiredChannels(chat, uid)
	case strings.HasPrefix(c.Data, "stars:"):
		days, _ := strconv.Atoi(strings.TrimPrefix(c.Data, "stars:"))
		if !validDays(days) {
			return
		}
		a.createStars(chat, uid, days)
	case strings.HasPrefix(c.Data, "crypto:"):
		parts := strings.Split(c.Data, ":")
		if len(parts) == 3 {
			days, _ := strconv.Atoi(parts[2])
			if validDays(days) {
				a.createCryptoOrder(chat, uid, parts[1], days)
			}
		}
	case strings.HasPrefix(c.Data, "days:"):
		days, _ := strconv.Atoi(strings.TrimPrefix(c.Data, "days:"))
		if validDays(days) {
			a.showPaymentMethods(chat, days)
		}
	case strings.HasPrefix(c.Data, "check:"):
		a.showOrderStatus(chat, uid, strings.TrimPrefix(c.Data, "check:"))
	}
}
func validDays(d int) bool { return d == 7 || d == 30 || d == 90 }
func (a *App) showPaymentMethods(chat int64, days int) {
	rows := [][]map[string]string{{button("Telegram Stars · "+strconv.Itoa(starsPrice(days)), fmt.Sprintf("stars:%d", days), "primary")}}
	if a.cryptoEnabled() {
		rows = append(rows, []map[string]string{button("TON", fmt.Sprintf("crypto:TON:%d", days), ""), button("USDT · TON", fmt.Sprintf("crypto:USDT:%d", days), "")})
	}
	var balance int64
	_ = a.db.QueryRow(`SELECT balance_rub FROM user_meta WHERE tg_id=?`, chat).Scan(&balance)
	if balance >= a.priceRub(days) {
		rows = append(rows, []map[string]string{button(fmt.Sprintf("Оплатить с баланса · %d ₽", a.priceRub(days)), fmt.Sprintf("paybalance:%d", days), "success")})
	}
	rows = append(rows, []map[string]string{button("Назад", "buy", ""), button("Главная", "home", "")})
	a.send(chat, fmt.Sprintf("<b>Оплата</b>\n\nVPN-доступ на <b>%d дней</b>\n\nВыбери способ оплаты.", days), inline(rows...))
}
func (a *App) showHelp(chat int64) {
	support := strings.TrimPrefix(a.cfg.Support, "@")
	text := "<b>Поддержка</b>\n\nЕсли возникла проблема с оплатой или подключением, напиши нам."
	rows := [][]map[string]string{{button("Назад", "home", "")}}
	if support != "" {
		rows = append([][]map[string]string{{urlButton("Написать в поддержку", "https://t.me/"+url.PathEscape(support), "primary")}}, rows...)
	}
	a.send(chat, text, inline(rows...))
}
func (a *App) showAdmin(chat, uid int64) {
	if strconv.FormatInt(uid, 10) != a.cfg.AdminID {
		a.home(chat)
		return
	}
	var users, paid, active, pending int
	_ = a.db.QueryRow(`SELECT count(*) FROM users`).Scan(&users)
	_ = a.db.QueryRow(`SELECT count(*) FROM orders WHERE status IN ('paid','active')`).Scan(&paid)
	_ = a.db.QueryRow(`SELECT count(*) FROM orders WHERE status='active'`).Scan(&active)
	_ = a.db.QueryRow(`SELECT count(*) FROM orders WHERE status='pending'`).Scan(&pending)
	a.send(chat, fmt.Sprintf("<b>Администрирование</b>\n\nПользователи: <b>%d</b>\nОплачено заказов: <b>%d</b>\nАктивные подписки: <b>%d</b>\nОжидают оплаты: <b>%d</b>", users, paid, active, pending), inline([]map[string]string{button("Пользователи", "admin:users", "primary")}, []map[string]string{button("Обязательная подписка", "admin:channels", "")}, []map[string]string{button("Курсы валют", "admin:rates", "")}, []map[string]string{button("Цены тарифов", "admin:prices", "")}, []map[string]string{button("Заказы", "admin:orders", "")}, []map[string]string{button("Обновить", "admin", ""), button("Главная", "home", "")}))
}

func (a *App) showBuy(chat int64) {
	rows := [][]map[string]string{
		{button(fmt.Sprintf("7 дней · %d ⭐", starsPrice(7)), "days:7", "")},
		{button(fmt.Sprintf("30 дней · %d ⭐", starsPrice(30)), "days:30", "primary")},
		{button(fmt.Sprintf("90 дней · %d ⭐", starsPrice(90)), "days:90", "")},
		{button("Назад", "home", "")},
	}
	a.send(chat, "<b>Тарифы</b>\n\nВыбери срок доступа.\n\n<blockquote>30 дней — оптимальный вариант для постоянного использования.</blockquote>", inline(rows...))
}
func (a *App) showStatus(chat int64, id int64) {
	var status, subURL, exp string
	e := a.db.QueryRow(`SELECT status,subscription_url,expires_at FROM orders WHERE tg_id=? AND status='active' AND subscription_url<>'' ORDER BY created_at DESC LIMIT 1`, id).Scan(&status, &subURL, &exp)
	if e != nil {
		a.send(chat, "<b>Мой VPN</b>\n\nАктивная подписка не найдена. Выбери тариф, чтобы подключиться.", inline([]map[string]string{button("Купить доступ", "buy", "primary")}, []map[string]string{button("Главная", "home", "")}))
		return
	}
	var paused int
	_ = a.db.QueryRow(`SELECT vpn_paused FROM user_meta WHERE tg_id=?`, id).Scan(&paused)
	state := "активен"
	if paused == 1 {
		state = "приостановлен администратором"
	}
	text := "<b>Мой VPN</b>\n\nСтатус: <b>" + state + "</b>\nДействует до: <b>" + html.EscapeString(exp) + "</b>\n\nСсылка подписки хранится конфиденциально. Не пересылай её другим."
	rows := [][]map[string]string{}
	if paused == 0 {
		rows = append(rows, []map[string]string{urlButton("Открыть подписку", subURL, "success")})
	}
	rows = append(rows, []map[string]string{button("Обновить", "status", ""), button("Продлить", "buy", "primary")}, []map[string]string{button("Главная", "home", "")})
	a.send(chat, text, inline(rows...))
}
func (a *App) showOrderStatus(chat, uid int64, orderID string) {
	var status string
	err := a.db.QueryRow(`SELECT status FROM orders WHERE id=? AND tg_id=?`, orderID, uid).Scan(&status)
	if err != nil {
		a.home(chat)
		return
	}
	label := map[string]string{"pending": "Ожидает оплаты", "paid": "Платёж подтверждён, выдаём доступ", "active": "Доступ активен", "expired": "Срок оплаты истёк"}[status]
	if label == "" {
		label = "Заказ обрабатывается"
	}
	a.send(chat, "<b>Статус заказа</b>\n\n"+html.EscapeString(label), inline([]map[string]string{button("Обновить статус", "check:"+orderID, ""), button("Главная", "home", "")}))
}

func (a *App) preCheckout(q tgPreCheckout) {
	var tg int64
	var status string
	var expected int
	e := a.db.QueryRow(`SELECT tg_id,status,CAST(expected_amount AS INTEGER) FROM orders WHERE id=?`, q.Payload).Scan(&tg, &status, &expected)
	ok := e == nil && tg == q.From.ID && status == "pending" && q.Currency == "XTR" && q.Total == expected
	p := map[string]any{"pre_checkout_query_id": q.ID, "ok": ok}
	if !ok {
		p["error_message"] = "Заказ не найден или уже обработан. Создай новый заказ."
	}
	if e := a.tg("answerPreCheckoutQuery", p, nil); e != nil {
		log.Printf("precheckout: %v", e)
	}
}
func (a *App) handleStars(m tgMessage) {
	p := m.SuccessfulPayment
	if p.Currency != "XTR" {
		return
	}
	var uid int64
	var days int
	var status string
	e := a.db.QueryRow(`SELECT tg_id,days,status FROM orders WHERE id=?`, p.Payload).Scan(&uid, &days, &status)
	var expected int
	if e == nil {
		e = a.db.QueryRow(`SELECT CAST(expected_amount AS INTEGER) FROM orders WHERE id=?`, p.Payload).Scan(&expected)
	}
	if e != nil || uid != m.From.ID || status != "pending" || p.Total != expected {
		log.Printf("unmatched Stars payment payload=%s uid=%d amount=%d", p.Payload, m.From.ID, p.Total)
		return
	}
	_, e = a.db.Exec(`UPDATE orders SET status='paid',tx_hash=?,updated_at=? WHERE id=? AND status='pending'`, p.ChargeID, time.Now().Unix(), p.Payload)
	if e != nil {
		log.Printf("save stars: %v", e)
		return
	}
	a.fulfill(p.Payload, uid, days, m.Chat.ID)
}
func (a *App) createStars(chat int64, uid int64, days int) {
	id := fmt.Sprintf("stars-%d-%d", uid, time.Now().UnixNano())
	amount := starsPrice(days)
	_, e := a.db.Exec(`INSERT INTO orders(id,tg_id,days,method,status,expected_amount,currency,created_at,updated_at) VALUES(?,?,?,'stars','pending',?,'XTR',?,?)`, id, uid, days, amount, time.Now().Unix(), time.Now().Unix())
	if e != nil {
		a.send(chat, "Не удалось создать заказ. Попробуй ещё раз.", inline([]map[string]string{button("Назад", "home", "")}))
		return
	}
	prices := []map[string]any{{"label": fmt.Sprintf("VPN на %d дней", days), "amount": amount}}
	var invoiceURL string
	// An invoice link keeps the bot's main UI message intact. The user opens Telegram's
	// native payment sheet from a button rather than receiving a second invoice message.
	if e = a.tg("createInvoiceLink", map[string]any{"title": "впн от пятерочки", "description": fmt.Sprintf("VPN-доступ на %d дней", days), "payload": id, "provider_token": "", "currency": "XTR", "prices": prices, "start_parameter": "vpn"}, &invoiceURL); e != nil || invoiceURL == "" {
		log.Printf("create invoice link: %v", e)
		_, _ = a.db.Exec(`UPDATE orders SET status='failed',updated_at=? WHERE id=? AND status='pending'`, time.Now().Unix(), id)
		a.send(chat, "Не удалось создать счёт Telegram Stars. Попробуй позже.", inline([]map[string]string{button("Назад", "home", "")}))
		return
	}
	a.send(chat, fmt.Sprintf("<b>Заказ создан</b>\n\nVPN-доступ на <b>%d дней</b>\nСтоимость: <b>%d Stars</b>\n\nНажми кнопку ниже, чтобы открыть защищённую форму оплаты Telegram. После подтверждения оплаты доступ будет выдан автоматически.", days, amount), inline([]map[string]string{urlButton("Оплатить Stars", invoiceURL, "success")}, []map[string]string{button("Проверить статус", "check:"+id, ""), button("Назад", "buy", "")}))
}

func starsPrice(days int) int {
	switch days {
	case 7:
		return 35
	case 30:
		return 120
	case 90:
		return 320
	default:
		return 120
	}
}
func (a *App) cryptoEnabled() bool {
	if a.cfg.TONWallet == "" || a.cfg.TONAPIKey == "" || a.cfg.USDTMaster == "" {
		return false
	}
	ton, usdt, updated := a.rates()
	return ton > 0 && usdt > 0 && !updated.IsZero() && time.Since(updated) < 2*time.Hour
}

func cryptoPrice(asset string, days int, cfg Config) string {
	var v string
	switch asset {
	case "TON":
		switch days {
		case 7:
			v = cfg.TONPrice7
		case 30:
			v = cfg.TONPrice30
		case 90:
			v = cfg.TONPrice90
		}
	case "USDT":
		switch days {
		case 7:
			v = cfg.USDTPrice7
		case 30:
			v = cfg.USDTPrice30
		case 90:
			v = cfg.USDTPrice90
		}
	}
	if v == "" {
		return ""
	}
	// Must be an integer amount in the asset's smallest unit.
	n, err := strconv.ParseUint(v, 10, 64)
	if err != nil || n == 0 {
		return ""
	}
	return strconv.FormatUint(n, 10)
}

func (a *App) createCryptoOrder(chat int64, uid int64, asset string, days int) {
	asset = strings.ToUpper(strings.TrimSpace(asset))
	if days != 7 && days != 30 && days != 90 {
		days = 30
	}
	if !a.cryptoEnabled() {
		a.send(chat, "Криптооплата пока не настроена. Выбери Telegram Stars.", inline([]map[string]string{button("Купить через Stars", "buy", "primary")}, []map[string]string{button("Главная", "home", "")}))
		return
	}
	amount := a.dynamicCryptoPrice(asset, days)
	if amount == "" || (asset != "TON" && asset != "USDT") {
		a.send(chat, "Этот способ оплаты сейчас недоступен. Выбери другой способ.", inline([]map[string]string{button("Назад", "buy", "")}, []map[string]string{button("Главная", "home", "")}))
		return
	}
	id := fmt.Sprintf("crypto-%d-%d", uid, time.Now().UnixNano())
	memo := "vpn-" + id
	now := time.Now().Unix()
	_, err := a.db.Exec(`INSERT INTO orders(id,tg_id,days,method,status,expected_amount,currency,created_at,updated_at,payment_memo) VALUES(?,?,?,?,'pending',?,?,?,?,?)`, id, uid, days, asset, amount, asset, now, now, memo)
	if err != nil {
		a.send(chat, "Не удалось создать заказ. Попробуй ещё раз.", inline([]map[string]string{button("Повторить", "buy", "primary")}, []map[string]string{button("Главная", "home", "")}))
		return
	}
	displayAmount := formatCryptoAmount(asset, amount)
	message := fmt.Sprintf("<b>Оплата %s</b>\n\nVPN-доступ: <b>%d дней</b>\nСеть: <b>TON</b>\nАктив: <b>%s</b>\nСумма: <code>%s %s</code>\n\nКошелёк для перевода:\n<code>%s</code>\n\nКомментарий к переводу:\n<code>%s</code>\n\nПереведи точную сумму и обязательно укажи комментарий. Заказ действует 30 минут. Доступ появится после подтверждения транзакции.", asset, days, asset, html.EscapeString(displayAmount), asset, html.EscapeString(a.cfg.TONWallet), html.EscapeString(memo))
	a.send(chat, message, inline([]map[string]string{button("Проверить оплату", "check:"+id, "primary")}, []map[string]string{button("Назад", "buy", ""), button("Главная", "home", "")}))
}

func (a *App) fulfill(orderID string, uid int64, days int, chat int64) {
	key := strings.ReplaceAll(orderID, " ", "")
	external := fmt.Sprintf("tg-%d", uid)
	var current partnerUser
	// Quote first, then cap the charge to the quoted amount. Never trust a price sent by Telegram.
	var quote struct {
		AmountMinor   string `json:"amount_minor"`
		Currency      string `json:"currency"`
		OrdersEnabled bool   `json:"orders_enabled"`
	}
	if e := a.partner("/quotes", "POST", map[string]any{"plan_id": a.cfg.PlanID, "days": days}, "", &quote); e != nil {
		log.Printf("quote failed order=%s: %v", orderID, e)
		a.notifyFailure(orderID, chat, "Платёж получен, но сервис временно не смог рассчитать заказ. Повторная выдача будет выполнена автоматически.")
		return
	}
	if quote.AmountMinor == "" || quote.Currency != "RUB" || !quote.OrdersEnabled {
		log.Printf("quote invalid order=%s currency=%s", orderID, quote.Currency)
		a.notifyFailure(orderID, chat, "Платёж получен, но тариф временно недоступен. Заказ сохранён, выдача будет повторена автоматически.")
		return
	}
	body := map[string]any{"plan_id": a.cfg.PlanID, "days": days, "max_charge_minor": quote.AmountMinor, "metadata": map[string]any{"tg_id": uid, "source": "telegram_bot", "order_id": orderID}}
	e := a.partner("/users/"+url.PathEscape(external), "PUT", body, "vpn-"+key, &current)
	if e != nil {
		log.Printf("FULFILLMENT FAILED order=%s: %v", orderID, e)
		a.notifyFailure(orderID, chat, "Оплата получена, но выдача VPN временно не удалась. Заказ сохранён; обратись в поддержку и не оплачивай повторно.")
		return
	}
	_, e = a.db.Exec(`UPDATE orders SET status='active',subscription_url=?,expires_at=?,partner_order_id=?,updated_at=? WHERE id=?`, current.SubscriptionURL, current.ExpiresAt, current.OrderID, time.Now().Unix(), orderID)
	if e != nil {
		log.Printf("persist fulfillment: %v", e)
	}
	a.awardReferral(uid, days)
	a.send(chat, "<b>VPN готов</b>\n\nОплата подтверждена. Доступ активирован.\n\nСсылка подписки:\n<code>"+html.EscapeString(current.SubscriptionURL)+"</code>\n\nДобавь ссылку в Happ или другое приложение с поддержкой подписок. Не пересылай её другим.", inline([]map[string]string{urlButton("Открыть подписку", current.SubscriptionURL, "success")}, []map[string]string{button("Мой VPN", "status", ""), button("Главная", "home", "")}))
}

func (a *App) notifyFailure(orderID string, chat int64, message string) {
	now := time.Now().Unix()
	res, err := a.db.Exec(`UPDATE orders SET notified_at=? WHERE id=? AND (notified_at=0 OR notified_at<?)`, now, orderID, now-300)
	if err != nil {
		log.Printf("notification throttle order=%s: %v", orderID, err)
		return
	}
	n, _ := res.RowsAffected()
	if n == 1 {
		a.send(chat, message, inline([]map[string]string{button("Главная", "home", "")}))
	}
}

// fulfillmentWorker recovers orders whose payment was confirmed but whose Partner API
// request failed or the process stopped between payment confirmation and delivery.
func (a *App) fulfillmentWorker() {
	for range time.NewTicker(20 * time.Second).C {
		rows, err := a.db.Query(`SELECT id,tg_id,days FROM orders WHERE status='paid' ORDER BY created_at LIMIT 25`)
		if err != nil {
			log.Printf("fulfillment queue: %v", err)
			continue
		}
		type job struct {
			id   string
			uid  int64
			days int
		}
		jobs := make([]job, 0, 25)
		for rows.Next() {
			var j job
			if rows.Scan(&j.id, &j.uid, &j.days) == nil {
				jobs = append(jobs, j)
			}
		}
		rows.Close()
		for _, j := range jobs {
			a.fulfill(j.id, j.uid, j.days, j.uid)
		}
	}
}

// TONAPI event parsing is fail-closed: a payment is accepted only if a successful
// transfer matches the configured recipient, exact amount, asset master, and unique memo.
// Keep TONAPI credentials private and monitor the watcher in production.
type tonEventsResponse struct {
	Events []json.RawMessage `json:"events"`
}

type transferFields struct {
	Sender struct {
		Address string `json:"address"`
	} `json:"sender"`
	Recipient struct {
		Address string `json:"address"`
	} `json:"recipient"`
	Amount  json.Number `json:"amount"`
	Comment string      `json:"comment"`
	Jetton  struct {
		Address  string `json:"address"`
		Decimals int    `json:"decimals"`
	} `json:"jetton"`
}

func rawField(m map[string]json.RawMessage, keys ...string) json.RawMessage {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			return v
		}
	}
	return nil
}
func parseAddress(raw json.RawMessage) string {
	var s string
	if len(raw) > 0 && raw[0] == '"' {
		_ = json.Unmarshal(raw, &s)
		return s
	}
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	if v, ok := m["address"].(string); ok {
		return v
	}
	return ""
}
func parseTransfer(raw json.RawMessage, asset string) (memo, amount, recipient, master, txHash, status string) {
	var ev map[string]json.RawMessage
	if json.Unmarshal(raw, &ev) != nil {
		return
	}
	if v := rawField(ev, "event_id"); len(v) > 0 {
		_ = json.Unmarshal(v, &txHash)
	}
	if v := rawField(ev, "is_scam"); len(v) > 0 {
		var scam bool
		_ = json.Unmarshal(v, &scam)
		if scam {
			return
		}
	}
	if v := rawField(ev, "actions"); len(v) > 0 {
		var actions []json.RawMessage
		if json.Unmarshal(v, &actions) != nil {
			return
		}
		for _, ar := range actions {
			var am map[string]json.RawMessage
			if json.Unmarshal(ar, &am) != nil {
				continue
			}
			var typ string
			_ = json.Unmarshal(rawField(am, "type"), &typ)
			var st string
			_ = json.Unmarshal(rawField(am, "status"), &st)
			if st != "ok" {
				continue
			}
			var tr transferFields
			var trRaw json.RawMessage
			if asset == "TON" && (strings.EqualFold(typ, "TonTransfer") || strings.EqualFold(typ, "ton_transfer")) {
				trRaw = rawField(am, "TonTransfer", "ton_transfer")
			}
			if asset == "USDT" && (strings.EqualFold(typ, "JettonTransfer") || strings.EqualFold(typ, "jetton_transfer")) {
				trRaw = rawField(am, "JettonTransfer", "jetton_transfer")
			}
			if len(trRaw) == 0 || json.Unmarshal(trRaw, &tr) != nil {
				continue
			}
			memo = tr.Comment
			amount = tr.Amount.String()
			recipient = tr.Recipient.Address
			master = tr.Jetton.Address
			status = st
			if memo != "" {
				return
			}
		}
	}
	return
}

func (a *App) tonPaymentWatcher() {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		if err := a.scanChainPayments(); err != nil {
			log.Printf("chain payment scan failed: %v", err)
		}
		<-ticker.C
	}
}
func (a *App) scanChainPayments() error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	endpoint := a.cfg.TONAPIBase + "/v2/accounts/" + url.PathEscape(a.cfg.TONWallet) + "/events?limit=100&subject_only=true"
	req, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+a.cfg.TONAPIKey)
	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("TONAPI HTTP %d: %s", resp.StatusCode, string(b))
	}
	var events tonEventsResponse
	if err = json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&events); err != nil {
		return err
	}
	for _, ev := range events.Events {
		for _, asset := range []string{"TON", "USDT"} {
			memo, amount, recipient, master, txHash, status := parseTransfer(ev, asset)
			if memo == "" || amount == "" || txHash == "" || status != "ok" {
				continue
			}
			// The event endpoint is scoped to the receiving wallet; still require exact recipient.
			if !sameAddress(recipient, a.cfg.TONWallet) {
				continue
			}
			if asset == "USDT" && !sameAddress(master, a.cfg.USDTMaster) {
				continue
			}
			var id string
			var uid int64
			var days int
			var expected string
			var created int64
			err := a.db.QueryRow(`SELECT id,tg_id,days,expected_amount,created_at FROM orders WHERE payment_memo=? AND method=? AND status='pending'`, memo, asset).Scan(&id, &uid, &days, &expected, &created)
			if err != nil {
				continue
			}
			if time.Now().Unix()-created > 30*60 {
				_, _ = a.db.Exec(`UPDATE orders SET status='expired',updated_at=? WHERE id=? AND status='pending'`, time.Now().Unix(), id)
				continue
			}
			if amount != expected {
				log.Printf("payment amount mismatch memo=%s asset=%s", memo, asset)
				continue
			}
			res, err := a.db.Exec(`UPDATE orders SET status='paid',tx_hash=?,updated_at=? WHERE id=? AND status='pending'`, txHash, time.Now().Unix(), id)
			if err != nil {
				log.Printf("save chain payment: %v", err)
				continue
			}
			n, _ := res.RowsAffected()
			if n == 1 {
				a.send(uid, fmt.Sprintf("<b>Платёж %s подтверждён</b>\n\nГотовлю VPN-доступ…", html.EscapeString(asset)), inline([]map[string]string{button("Главная", "home", "")}))
				a.fulfill(id, uid, days, uid)
			}
		}
	}
	return nil
}
func sameAddress(a, b string) bool {
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}

func verifyHMAC(secret, timestamp string, body []byte, sig string) bool {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp + "."))
	mac.Write(body)
	want := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(want), []byte(sig))
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
