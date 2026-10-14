package main

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

func (a *App) isAdmin(uid int64) bool {
	return a.cfg.AdminID != "" && strconv.FormatInt(uid, 10) == a.cfg.AdminID
}
func (a *App) priceRub(days int) int64 {
	var override string
	if a.db != nil {
		_ = a.db.QueryRow(`SELECT value FROM app_settings WHERE key=?`, fmt.Sprintf("price_%d_rub", days)).Scan(&override)
		if n, err := strconv.ParseInt(override, 10, 64); err == nil && n > 0 {
			return n
		}
	}
	v := a.cfg.RubPrice30
	switch days {
	case 7:
		v = a.cfg.RubPrice7
	case 90:
		v = a.cfg.RubPrice90
	}
	n, _ := strconv.ParseInt(v, 10, 64)
	if n < 1 {
		return 149
	}
	return n
}

func (a *App) exchangeRateWorker() {
	for {
		ctx, cancel := contextWithTimeout()
		req, err := http.NewRequestWithContext(ctx, "GET", "https://api.coingecko.com/api/v3/simple/price?ids=the-open-network,tether&vs_currencies=rub", nil)
		if err == nil {
			resp, e := a.client.Do(req)
			if e == nil {
				var data map[string]map[string]float64
				if resp.StatusCode >= 200 && resp.StatusCode < 300 && json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&data) == nil {
					t, ok1 := data["the-open-network"]["rub"]
					u, ok2 := data["tether"]["rub"]
					if ok1 && ok2 && t > 0 && u > 0 {
						now := time.Now().Unix()
						_, _ = a.db.Exec(`INSERT INTO exchange_rates(asset,rub,updated_at) VALUES('TON',?,?) ON CONFLICT(asset) DO UPDATE SET rub=excluded.rub,updated_at=excluded.updated_at`, t, now)
						_, _ = a.db.Exec(`INSERT INTO exchange_rates(asset,rub,updated_at) VALUES('USDT',?,?) ON CONFLICT(asset) DO UPDATE SET rub=excluded.rub,updated_at=excluded.updated_at`, u, now)
						a.rateMu.Lock()
						a.tonRub = t
						a.usdtRub = u
						a.rateUpdated = time.Now()
						a.rateMu.Unlock()
					}
				}
				resp.Body.Close()
			}
		}
		cancel()
		if err != nil {
			log.Printf("exchange-rate request: %v", err)
		}
		time.Sleep(10 * time.Minute)
	}
}

// Kept separate so requests have a bounded lifetime and don't hold shutdown paths forever.
func contextWithTimeout() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 12*time.Second)
}

func (a *App) rates() (float64, float64, time.Time) {
	a.rateMu.RLock()
	t, u, at := a.tonRub, a.usdtRub, a.rateUpdated
	a.rateMu.RUnlock()
	if t > 0 && u > 0 {
		return t, u, at
	}
	var tt, uu float64
	var ts, us int64
	_ = a.db.QueryRow(`SELECT rub,updated_at FROM exchange_rates WHERE asset='TON'`).Scan(&tt, &ts)
	_ = a.db.QueryRow(`SELECT rub,updated_at FROM exchange_rates WHERE asset='USDT'`).Scan(&uu, &us)
	if tt > 0 && uu > 0 {
		return tt, uu, time.Unix(minInt64(ts, us), 0)
	}
	return 0, 0, time.Time{}
}
func minInt64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func (a *App) showRates(chat, uid int64) {
	if !a.isAdmin(uid) {
		a.home(chat)
		return
	}
	t, u, at := a.rates()
	text := "<b>Курсы валют</b>\n\nКурс обновляется автоматически каждые 10 минут через CoinGecko. Цены тарифов задаются в рублях в .env, криптоцена пересчитывается по текущему курсу."
	if t > 0 && u > 0 {
		text += fmt.Sprintf("\n\nTON: <b>%.2f ₽</b>\nUSDT: <b>%.2f ₽</b>\nОбновлено: <code>%s UTC</code>", t, u, at.UTC().Format("2006-01-02 15:04:05"))
	} else {
		text += "\n\nКурс ещё не получен. Проверь доступ сервера к api.coingecko.com."
	}
	text += fmt.Sprintf("\n\nТарифы: 7 дней — %d ₽; 30 дней — %d ₽; 90 дней — %d ₽.", a.priceRub(7), a.priceRub(30), a.priceRub(90))
	a.send(chat, text, inline([]map[string]string{button("Обновить", "admin:rates", "primary")}, []map[string]string{button("В админку", "admin", "")}))
}

func (a *App) showAdminUsers(chat, uid int64, page int) {
	if !a.isAdmin(uid) {
		a.home(chat)
		return
	}
	if page < 0 {
		page = 0
	}
	rowsDB, err := a.db.Query(`SELECT u.tg_id,COALESCE(u.username,''),COALESCE(m.balance_rub,0),COALESCE(m.blocked,0) FROM users u LEFT JOIN user_meta m ON m.tg_id=u.tg_id ORDER BY u.created_at DESC LIMIT 8 OFFSET ?`, page*8)
	if err != nil {
		a.send(chat, "Не удалось загрузить пользователей.", inline([]map[string]string{button("Назад", "admin", "")}))
		return
	}
	rows := [][]map[string]string{}
	for rowsDB.Next() {
		var id int64
		var username string
		var bal, blocked int64
		if rowsDB.Scan(&id, &username, &bal, &blocked) == nil {
			name := fmt.Sprintf("%d · %d ₽", id, bal)
			if username != "" {
				name = "@" + username + " · " + fmt.Sprintf("%d ₽", bal)
			}
			if blocked == 1 {
				name += " · отключён"
			}
			rows = append(rows, []map[string]string{button(name, "admin:user:"+strconv.FormatInt(id, 10), "")})
		}
	}
	rowsDB.Close()
	nav := []map[string]string{}
	if page > 0 {
		nav = append(nav, button("Назад", "admin:users:"+strconv.Itoa(page-1), ""))
	}
	nav = append(nav, button("Далее", "admin:users:"+strconv.Itoa(page+1), ""))
	rows = append(rows, nav, []map[string]string{button("В админку", "admin", "")})
	var total int
	_ = a.db.QueryRow(`SELECT count(*) FROM users`).Scan(&total)
	a.send(chat, fmt.Sprintf("<b>Пользователи</b>\n\nВсего: %d\nСтраница: %d\n\nВыбери пользователя для управления.", total, page+1), inline(rows...))
}
func (a *App) showAdminUser(chat, admin, target int64) {
	if !a.isAdmin(admin) {
		a.home(chat)
		return
	}
	var username string
	var bal, blocked, paused int64
	var refs int
	err := a.db.QueryRow(`SELECT COALESCE(u.username,''),COALESCE(m.balance_rub,0),COALESCE(m.blocked,0),COALESCE(m.vpn_paused,0) FROM users u LEFT JOIN user_meta m ON m.tg_id=u.tg_id WHERE u.tg_id=?`, target).Scan(&username, &bal, &blocked, &paused)
	if err != nil {
		a.showAdminUsers(chat, admin, 0)
		return
	}
	_ = a.db.QueryRow(`SELECT count(*) FROM user_meta WHERE referrer_id=?`, target).Scan(&refs)
	state := "активен"
	if blocked == 1 {
		state = "отключён"
	}
	name := fmt.Sprintf("<b>Пользователь %d</b>", target)
	if username != "" {
		name += "\n@" + html.EscapeString(username)
	}
	text := fmt.Sprintf("%s\n\nБаланс: <b>%d ₽</b>\nСтатус бота: <b>%s</b>\nVPN: <b>%s</b>\nПриглашено пользователей: <b>%d</b>\n\nБаланс и доступ управляются кнопками ниже.", name, bal, state, map[bool]string{true: "приостановлен", false: "активен"}[paused == 1], refs)
	id := strconv.FormatInt(target, 10)
	rows := [][]map[string]string{{button("+100 ₽", "admin:bal:add:"+id+":100", "success"), button("+500 ₽", "admin:bal:add:"+id+":500", "success")}, {button("−100 ₽", "admin:bal:sub:"+id+":100", ""), button("−500 ₽", "admin:bal:sub:"+id+":500", "")}, {button(map[bool]string{true: "Включить пользователя", false: "Отключить пользователя"}[blocked == 1], "admin:block:"+id, "primary")}}
	if paused == 1 {
		rows = append(rows, []map[string]string{button("Возобновить VPN", "admin:vpn:enable:"+id, "success")})
	} else {
		rows = append(rows, []map[string]string{button("Приостановить VPN", "admin:vpn:disable:"+id, "primary")})
	}
	rows = append(rows, []map[string]string{button("Список пользователей", "admin:users", ""), button("В админку", "admin", "")})
	a.send(chat, text, inline(rows...))
}
func (a *App) adjustBalance(chat, admin, target int64, delta int64) {
	if !a.isAdmin(admin) {
		a.home(chat)
		return
	}
	// The update is atomic and clamps the balance at zero.
	_, err := a.db.Exec(`INSERT INTO user_meta(tg_id,balance_rub,created_at) VALUES(?,?,?) ON CONFLICT(tg_id) DO UPDATE SET balance_rub=MAX(0,user_meta.balance_rub+?)`, target, max64(0, delta), time.Now().Unix(), delta)
	if err != nil {
		a.send(chat, "Не удалось изменить баланс.", inline([]map[string]string{button("Назад", "admin:user:"+strconv.FormatInt(target, 10), "")}))
		return
	}
	a.showAdminUser(chat, admin, target)
}
func (a *App) toggleBlock(chat, admin, target int64) {
	if !a.isAdmin(admin) {
		a.home(chat)
		return
	}
	_, _ = a.db.Exec(`INSERT INTO user_meta(tg_id,blocked,created_at) VALUES(?,1,?) ON CONFLICT(tg_id) DO UPDATE SET blocked=CASE WHEN blocked=1 THEN 0 ELSE 1 END`, target, time.Now().Unix())
	a.showAdminUser(chat, admin, target)
}
func (a *App) showAdminChannels(chat, uid int64) {
	if !a.isAdmin(uid) {
		a.home(chat)
		return
	}
	rowsDB, err := a.db.Query(`SELECT chat_id,title FROM required_channels ORDER BY added_at`)
	rows := [][]map[string]string{}
	if err == nil {
		for rowsDB.Next() {
			var id, title string
			if rowsDB.Scan(&id, &title) == nil {
				label := title
				if label == "" {
					label = id
				}
				rows = append(rows, []map[string]string{button("Удалить · "+label, "admin:channel:del:"+id, "")})
			}
		}
		rowsDB.Close()
	}
	rows = append(rows, []map[string]string{button("Добавить канал", "admin:channel:add", "primary")}, []map[string]string{button("В админку", "admin", "")})
	a.send(chat, "<b>Обязательная подписка</b>\n\nДобавь публичный канал через @username. Бот должен быть администратором канала, иначе проверка подписки не сработает.", inline(rows...))
}
func (a *App) askAdminInput(chat, uid int64, action, payload string) {
	if !a.isAdmin(uid) {
		a.home(chat)
		return
	}
	_, _ = a.db.Exec(`INSERT INTO admin_inputs(tg_id,action,payload,created_at) VALUES(?,?,?,?) ON CONFLICT(tg_id) DO UPDATE SET action=excluded.action,payload=excluded.payload,created_at=excluded.created_at`, uid, action, payload, time.Now().Unix())
	a.send(chat, "<b>Добавление канала</b>\n\nОтправь @username публичного канала отдельным сообщением. Это единственный шаг, где нужен текстовый ввод.", inline([]map[string]string{button("Отмена", "admin:channels", "")}))
}
func (a *App) handleAdminInput(chat, uid int64, action, payload, input string) {
	if !a.isAdmin(uid) {
		return
	}
	_, _ = a.db.Exec(`DELETE FROM admin_inputs WHERE tg_id=?`, uid)
	if action == "price_set" {
		n, err := strconv.ParseInt(strings.TrimSpace(input), 10, 64)
		days, _ := strconv.Atoi(payload)
		if err != nil || n < 1 || n > 100000 || !validDays(days) {
			a.send(chat, "Укажи цену целым числом от 1 до 100000 рублей.", inline([]map[string]string{button("Повторить", "admin:price:set:"+payload, "primary"), button("Назад", "admin:prices", "")}))
			return
		}
		_, err = a.db.Exec(`INSERT INTO app_settings(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, fmt.Sprintf("price_%d_rub", days), strconv.FormatInt(n, 10))
		if err != nil {
			a.send(chat, "Не удалось сохранить цену.", inline([]map[string]string{button("Назад", "admin:prices", "")}))
			return
		}
		a.showAdminPrices(chat, uid)
		return
	}
	if action != "channel_add" {
		a.showAdmin(chat, uid)
		return
	}
	id := strings.TrimSpace(input)
	if id == "" || len(id) > 64 || !strings.HasPrefix(id, "@") || strings.ContainsAny(id, " \n\r") {
		a.send(chat, "Некорректный идентификатор канала.", inline([]map[string]string{button("Повторить", "admin:channel:add", "primary"), button("Назад", "admin:channels", "")}))
		return
	}
	_, err := a.db.Exec(`INSERT INTO required_channels(chat_id,title,added_at) VALUES(?,?,?) ON CONFLICT(chat_id) DO NOTHING`, id, id, time.Now().Unix())
	if err != nil {
		a.send(chat, "Не удалось сохранить канал.", inline([]map[string]string{button("Назад", "admin:channels", "")}))
		return
	}
	a.showAdminChannels(chat, uid)
}
func (a *App) deleteChannel(chat, uid int64, id string) {
	if !a.isAdmin(uid) {
		a.home(chat)
		return
	}
	_, _ = a.db.Exec(`DELETE FROM required_channels WHERE chat_id=?`, id)
	a.showAdminChannels(chat, uid)
}
func (a *App) hasRequiredChannels(uid int64) bool {
	rows, err := a.db.Query(`SELECT chat_id FROM required_channels`)
	if err != nil {
		return true
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if rows.Scan(&id) != nil {
			continue
		}
		var chatID any = id
		if n, e := strconv.ParseInt(id, 10, 64); e == nil {
			chatID = n
		}
		var member struct {
			Status   string `json:"status"`
			IsMember bool   `json:"is_member"`
		}
		if a.tg("getChatMember", map[string]any{"chat_id": chatID, "user_id": uid}, &member) != nil {
			return false
		}
		if member.Status != "creator" && member.Status != "administrator" && member.Status != "member" && !(member.Status == "restricted" && member.IsMember) {
			return false
		}
	}
	return true
}
func (a *App) showRequiredChannels(chat int64) {
	rowsDB, err := a.db.Query(`SELECT chat_id,title FROM required_channels`)
	rows := [][]map[string]string{}
	if err == nil {
		for rowsDB.Next() {
			var id, title string
			if rowsDB.Scan(&id, &title) == nil {
				link := "https://t.me/" + strings.TrimPrefix(id, "@")
				if strings.HasPrefix(id, "@") {
					rows = append(rows, []map[string]string{urlButton("Подписаться: "+id, link, "")})
				} else if title != "" {
					rows = append(rows, []map[string]string{button("Канал: "+title, "channels:check", "")})
				}
			}
		}
		rowsDB.Close()
	}
	rows = append(rows, []map[string]string{button("Проверить подписку", "channels:check", "primary")})
	a.send(chat, "<b>Подписка на каналы</b>\n\nЧтобы продолжить, подпишись на обязательные каналы и нажми «Проверить подписку».", inline(rows...))
}
func (a *App) checkRequiredChannels(chat, uid int64) {
	if a.hasRequiredChannels(uid) {
		a.home(chat)
	} else {
		a.showRequiredChannels(chat)
	}
}
func (a *App) showReferral(chat, uid int64) {
	var count int
	var balance int64
	_ = a.db.QueryRow(`SELECT count(*) FROM user_meta WHERE referrer_id=? AND referral_rewarded=1`, uid).Scan(&count)
	_ = a.db.QueryRow(`SELECT balance_rub FROM user_meta WHERE tg_id=?`, uid).Scan(&balance)
	var me struct {
		Username string `json:"username"`
	}
	_ = a.tg("getMe", map[string]any{}, &me)
	link := fmt.Sprintf("https://t.me/%s?start=ref_%d", me.Username, uid)
	bonus, _ := strconv.Atoi(a.cfg.ReferralBonusPercent)
	if bonus < 1 || bonus > 50 {
		bonus = 10
	}
	text := fmt.Sprintf("<b>Пригласить друга</b>\n\nОтправь другу ссылку. Когда он впервые оплатит VPN, ты получишь %d%% от стоимости тарифа на внутренний баланс.\n\nПриглашено с покупкой: <b>%d</b>\nТвой баланс: <b>%d ₽</b>\n\n<code>%s</code>", bonus, count, balance, html.EscapeString(link))
	a.send(chat, text, inline([]map[string]string{button("Баланс", "balance", "")}, []map[string]string{button("Главная", "home", "")}))
}
func (a *App) showBalance(chat, uid int64) {
	var bal int64
	_ = a.db.QueryRow(`SELECT balance_rub FROM user_meta WHERE tg_id=?`, uid).Scan(&bal)
	a.send(chat, fmt.Sprintf("<b>Баланс</b>\n\nДоступно: <b>%d ₽</b>\n\nБаланс пополняется реферальными начислениями или администратором. Его можно использовать для оплаты тарифа, если средств достаточно.", bal), inline([]map[string]string{button("Пригласить друга", "referral", "")}, []map[string]string{button("Главная", "home", "")}))
}

func (a *App) awardReferral(uid int64, days int) {
	var ref int64
	var rewarded int
	err := a.db.QueryRow(`SELECT referrer_id,referral_rewarded FROM user_meta WHERE tg_id=?`, uid).Scan(&ref, &rewarded)
	if err != nil || ref <= 0 || rewarded != 0 || ref == uid {
		return
	}
	percent, _ := strconv.ParseInt(a.cfg.ReferralBonusPercent, 10, 64)
	if percent < 1 || percent > 50 {
		percent = 10
	}
	bonus := a.priceRub(days) * percent / 100
	if bonus < 1 {
		return
	}
	tx, err := a.db.Begin()
	if err != nil {
		return
	}
	defer tx.Rollback()
	res, err := tx.Exec(`UPDATE user_meta SET referral_rewarded=1 WHERE tg_id=? AND referral_rewarded=0`, uid)
	if err != nil {
		return
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return
	}
	_, err = tx.Exec(`INSERT INTO user_meta(tg_id,balance_rub,created_at) VALUES(?,?,?) ON CONFLICT(tg_id) DO UPDATE SET balance_rub=user_meta.balance_rub+excluded.balance_rub`, ref, bonus, time.Now().Unix())
	if err != nil {
		return
	}
	if tx.Commit() == nil {
		a.send(ref, fmt.Sprintf("<b>Реферальное начисление</b>\n\nДруг впервые оплатил VPN. На баланс зачислено <b>%d ₽</b>.", bonus), inline([]map[string]string{button("Мой баланс", "balance", "primary"), button("Главная", "home", "")}))
	}
}

// The local account block is enforced on every incoming update. External subscription revocation
// requires a provider endpoint explicitly documented by the Partner API; this app does not guess it.

func (a *App) dynamicCryptoPrice(asset string, days int) string {
	ton, usdt, _ := a.rates()
	rub := float64(a.priceRub(days))
	var amount float64
	switch strings.ToUpper(asset) {
	case "TON":
		if ton > 0 {
			amount = rub / ton * 1e9
		} else {
			return cryptoPrice(asset, days, a.cfg)
		}
	case "USDT":
		if usdt > 0 {
			amount = rub / usdt * 1e6
		} else {
			return cryptoPrice(asset, days, a.cfg)
		}
	default:
		return ""
	}
	if amount < 1 || amount > 9e18 {
		return ""
	}
	return strconv.FormatUint(uint64(math.Round(amount)), 10)
}

func (a *App) payWithBalance(chat, uid int64, days int) {
	cost := a.priceRub(days)
	id := fmt.Sprintf("balance-%d-%d", uid, time.Now().UnixNano())
	now := time.Now().Unix()
	tx, err := a.db.Begin()
	if err != nil {
		a.send(chat, "Не удалось создать заказ. Попробуй ещё раз.", inline([]map[string]string{button("Назад", "buy", "")}))
		return
	}
	defer tx.Rollback()
	res, err := tx.Exec(`UPDATE user_meta SET balance_rub=balance_rub-? WHERE tg_id=? AND balance_rub>=?`, cost, uid, cost)
	if err != nil {
		return
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		a.send(chat, "На балансе недостаточно средств.", inline([]map[string]string{button("Назад", "buy", "")}))
		return
	}
	_, err = tx.Exec(`INSERT INTO orders(id,tg_id,days,method,status,expected_amount,currency,created_at,updated_at) VALUES(?,?,?,'balance','paid',?,'RUB',?,?)`, id, uid, days, cost, now, now)
	if err != nil {
		a.send(chat, "Не удалось создать заказ.", inline([]map[string]string{button("Назад", "buy", "")}))
		return
	}
	if err = tx.Commit(); err != nil {
		a.send(chat, "Не удалось подтвердить списание. Обратись в поддержку, если баланс изменился.", inline([]map[string]string{button("Главная", "home", "")}))
		return
	}
	a.send(chat, "<b>Заказ оплачен с баланса</b>\n\nСумма списания: <b>"+strconv.FormatInt(cost, 10)+" ₽</b>\nВыдаю VPN-доступ…", inline([]map[string]string{button("Проверить статус", "check:"+id, "primary"), button("Главная", "home", "")}))
	a.fulfill(id, uid, days, chat)
}

func (a *App) setVPNPaused(chat, admin, target int64, pause bool) {
	if !a.isAdmin(admin) {
		a.home(chat)
		return
	}
	external := fmt.Sprintf("tg-%d", target)
	path, method := "/users/"+url.PathEscape(external), "DELETE"
	if !pause {
		path += "/resume"
		method = "POST"
	}
	if err := a.partner(path, method, nil, "", nil); err != nil {
		log.Printf("admin VPN state change target=%d pause=%v: %v", target, pause, err)
		a.send(chat, "Не удалось изменить состояние VPN. Проверь, что пользователь существует в Partner API, и повтори попытку.", inline([]map[string]string{button("Назад", "admin:user:"+strconv.FormatInt(target, 10), "")}))
		return
	}
	val := 0
	if pause {
		val = 1
	}
	_, err := a.db.Exec(`INSERT INTO user_meta(tg_id,vpn_paused,created_at) VALUES(?,?,?) ON CONFLICT(tg_id) DO UPDATE SET vpn_paused=excluded.vpn_paused`, target, val, time.Now().Unix())
	if err != nil {
		log.Printf("persist vpn paused state: %v", err)
	}
	a.showAdminUser(chat, admin, target)
}

func formatCryptoAmount(asset, smallest string) string {
	n, err := strconv.ParseUint(smallest, 10, 64)
	if err != nil {
		return smallest
	}
	decimals := uint(9)
	if strings.EqualFold(asset, "USDT") {
		decimals = 6
	}
	scale := math.Pow10(int(decimals))
	value := float64(n) / scale
	formatted := strconv.FormatFloat(value, 'f', int(decimals), 64)
	formatted = strings.TrimRight(strings.TrimRight(formatted, "0"), ".")
	if formatted == "" {
		return "0"
	}
	return formatted
}

func (a *App) showAdminPrices(chat, uid int64) {
	if !a.isAdmin(uid) {
		a.home(chat)
		return
	}
	text := fmt.Sprintf("<b>Цены тарифов</b>\n\n7 дней: <b>%d ₽</b>\n30 дней: <b>%d ₽</b>\n90 дней: <b>%d ₽</b>\n\nИзменения применяются к новым заказам. Уже созданные заказы не пересчитываются.", a.priceRub(7), a.priceRub(30), a.priceRub(90))
	a.send(chat, text, inline([]map[string]string{button("Изменить 7 дней", "admin:price:set:7", "")}, []map[string]string{button("Изменить 30 дней", "admin:price:set:30", "primary")}, []map[string]string{button("Изменить 90 дней", "admin:price:set:90", "")}, []map[string]string{button("В админку", "admin", "")}))
}
func (a *App) showAdminOrders(chat, uid int64) {
	if !a.isAdmin(uid) {
		a.home(chat)
		return
	}
	rowsDB, err := a.db.Query(`SELECT id,tg_id,days,method,status,created_at FROM orders ORDER BY created_at DESC LIMIT 12`)
	if err != nil {
		a.send(chat, "Не удалось загрузить заказы.", inline([]map[string]string{button("В админку", "admin", "")}))
		return
	}
	lines := []string{"<b>Последние заказы</b>"}
	for rowsDB.Next() {
		var id, method, status string
		var user int64
		var days int
		var created int64
		if rowsDB.Scan(&id, &user, &days, &method, &status, &created) == nil {
			lines = append(lines, fmt.Sprintf("\n<code>%s</code>\nПользователь: <code>%d</code> · %d дней\n%s / %s · %s", html.EscapeString(id), user, days, html.EscapeString(method), html.EscapeString(status), time.Unix(created, 0).UTC().Format("02.01 15:04 UTC")))
		}
	}
	rowsDB.Close()
	a.send(chat, strings.Join(lines, "\n"), inline([]map[string]string{button("Обновить", "admin:orders", "primary"), button("В админку", "admin", "")}))
}
