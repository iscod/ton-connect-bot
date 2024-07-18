package main

import (
	"bytes"
	"context"
	"fmt"
	"github.com/caarlos0/env/v11"
	"github.com/cameo-engineering/tonconnect"
	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/skip2/go-qrcode"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"time"
)

type config struct {
	Token         string `env:"TOKEN"`
	ManifestUrl   string `env:"MANIFEST_URL"`
	RedisHost     string `env:"REDIS_HOST,notEmpty" envDefault:"localhost:6379"`
	RedisPassword string `env:"REDIS_PASSWORD"`
}

var cfg = config{}

func main() {
	if err := env.Parse(&cfg); err != nil {
		fmt.Printf("%+v\n", err)
		return
	}

	cfg.ManifestUrl = "https://iscod.github.io/tonconnect-manifest.json"

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	opts := []bot.Option{
		bot.WithDefaultHandler(defaultHandler),
	}
	fmt.Printf("bot tolen : %s\n", cfg.Token)
	b, err := bot.New(cfg.Token, opts...)
	if err != nil {
		panic(err)
	}

	b.RegisterHandler(bot.HandlerTypeMessageText, "/start", bot.MatchTypeExact, startHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/transaction", bot.MatchTypeExact, transactionHandler)
	b.RegisterHandler(bot.HandlerTypeCallbackQueryData, "transaction", bot.MatchTypePrefix, callBackHandler)
	b.RegisterHandler(bot.HandlerTypeCallbackQueryData, "connect", bot.MatchTypePrefix, callBackHandler)
	b.RegisterHandler(bot.HandlerTypeCallbackQueryData, "disconnect", bot.MatchTypePrefix, callBackHandler)

	b.Start(ctx)
}

func defaultHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	if update.Message == nil {
		log.Printf("defaultHandler message is nil %v", update)
		return
	}

	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: update.Message.Chat.ID,
		Text:   "Say /hello",
	})
}

func startHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	connector := getConnectWallet(update.Message.Chat.ID)
	message := &bot.SendMessageParams{ChatID: update.Message.Chat.ID, ParseMode: models.ParseModeHTML}
	rk := models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{}}
	if connector == nil {
		for _, wallet := range tonconnect.Wallets {
			buttons := []models.InlineKeyboardButton{{Text: wallet.Name, CallbackData: fmt.Sprintf("connect:%v", wallet.Name)}}
			rk.InlineKeyboard = append(rk.InlineKeyboard, buttons)
		}

		message.Text = "Choose wallet to connect"
		message.ReplyMarkup = rk
	} else {
		message.Text = "wallet is connected"
		buttons := []models.InlineKeyboardButton{{Text: "Send Transaction", CallbackData: "transaction"}, {Text: "Disconnect", CallbackData: "disconnect"}}
		rk.InlineKeyboard = append(rk.InlineKeyboard, buttons)
		message.ReplyMarkup = rk
	}

	b.SendMessage(ctx, message)
}

func callBackHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	message := update.CallbackQuery.Message
	data := update.CallbackQuery.Data
	switch data {
	case "start":
		startHandler(ctx, b, update)
	case "transaction":
		transactionHandler(ctx, b, update)
	case "disconnect":
		disconnectWallet(ctx, b, message)
	default:
		strs := strings.Split(data, ":")
		if strs[0] == "connect" {
			connectWallet(message, b, strs[1])
		}

	}
}

var connectSession sync.Map

func getConnectWallet(chatId int64) *tonconnect.Session {
	value, ok := connectSession.Load(chatId)
	if !ok {
		return nil
	}

	return value.(*tonconnect.Session)
}

func transactionHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	var chatId int64
	if update.Message != nil {
		chatId = update.Message.Chat.ID
	}

	if update.CallbackQuery != nil {
		chatId = update.CallbackQuery.Message.Message.Chat.ID
	}

	connector := getConnectWallet(chatId)
	if connector == nil {
		b.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatId, ParseMode: models.ParseModeHTML, Text: "Connect wallet first!"})
		return
	}

	msg, err := tonconnect.NewMessage("UQCdEJ1YAwYv0y7qASiBLmk2F98wetprW9pDrHjA7-onWWHq", "100000000")
	if err != nil {
		log.Fatal(err)
	}
	tx, err := tonconnect.NewTransaction(
		tonconnect.WithTimeout(3*time.Minute),
		tonconnect.WithTestnet(),
		tonconnect.WithMessage(*msg),
	)

	_, err = b.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatId, ParseMode: models.ParseModeHTML, Text: "Approve transaction in your wallet app!"})
	if err != nil {
		go func(chatID int64) {
			boc, err := connector.SendTransaction(ctx, *tx)
			if err != nil {
				b.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, ParseMode: models.ParseModeHTML, Text: fmt.Sprintf("Transaction err: %s", err)})
			} else {
				b.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, ParseMode: models.ParseModeHTML, Text: fmt.Sprintf("Transaction boc: %s", boc)})
			}
		}(chatId)
	}
}

func connectWallet(update models.MaybeInaccessibleMessage, b *bot.Bot, name string) {
	var wallet tonconnect.Wallet
	for _, w := range tonconnect.Wallets {
		if w.Name == name {
			wallet = w
		}
	}

	if wallet.Name == "" {
		return
	}

	connReq, err := tonconnect.NewConnectRequest(
		cfg.ManifestUrl, //"https://raw.githubusercontent.com/cameo-engineering/tonconnect/master/tonconnect-manifest.json",
		tonconnect.WithProofRequest(strconv.Itoa(int(update.Message.Chat.ID))),
	)

	if err != nil {
		log.Fatal(err)
	}

	s, err := tonconnect.NewSession()
	if err != nil {
		log.Printf("ton connect session error: %v", err)
		return
	}

	generatedURL, err := s.GenerateUniversalLink(wallet, *connReq)
	rk := models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{}}
	rk.InlineKeyboard = append(rk.InlineKeyboard, []models.InlineKeyboardButton{{Text: "Connect", URL: generatedURL}})

	png, err := qrcode.Encode(generatedURL, qrcode.Medium, 256)
	if err != nil {
		log.Printf("Error encoding %s\n", err)
		return
	}
	photo := &models.InputFileUpload{Filename: "qrcode.png", Data: bytes.NewReader(png)}

	message := &bot.SendPhotoParams{
		Photo:       photo,
		Caption:     "Connect wallet within 3 minutes",
		ChatID:      update.Message.Chat.ID,
		ParseMode:   models.ParseModeHTML,
		ReplyMarkup: rk,
	}

	sendMessage, err := b.SendPhoto(context.Background(), message)
	if err != nil {
		log.Printf("Send photo failed: %s\n", err)
		return
	}
	go func(chatId int64) {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		res, err := s.Connect(ctx, wallet)
		if err != nil {
			log.Printf("Connect failed: %s\n", err)
			b.DeleteMessage(context.Background(), &bot.DeleteMessageParams{MessageID: sendMessage.ID, ChatID: sendMessage.Chat.ID})
			b.SendMessage(context.Background(), &bot.SendMessageParams{
				Text:      "Timeout error!",
				ChatID:    update.Message.Chat.ID,
				ParseMode: models.ParseModeHTML,
			})
			return
		}

		var addr string
		network := "mainnet"
		for _, item := range res.Items {
			if item.Name == "ton_addr" {
				addr = item.Address
				if item.Network == -3 {
					network = "testnet"
				}
			}
			if item.Name == "ton_chat_id" {

			}
			fmt.Printf("connected %s, %v \n", item.Name, item.Proof)
			fmt.Printf("connected %s, %v \n", item.Name, item.WalletStateInit)
		}

		log.Printf(
			"%s %s for %s is connected to %s with %s address\n\n",
			res.Device.AppName,
			res.Device.AppVersion,
			res.Device.Platform,
			network,
			addr,
		)

		if addr != "" {
			log.Printf("Connected with address: %s", addr)
			_, err = b.SendMessage(context.Background(), &bot.SendMessageParams{
				Text:      fmt.Sprintf("You are connected with address <code>%s</code>", addr),
				ChatID:    update.Message.Chat.ID,
				ParseMode: models.ParseModeHTML,
			})
			if err != nil {
				log.Printf("Connect failed %s", err)
				return
			}
			connectSession.LoadOrStore(chatId, s)
		}
	}(update.Message.Chat.ID)
}

func disconnectWallet(ctx context.Context, b *bot.Bot, update models.MaybeInaccessibleMessage) {
	connector := getConnectWallet(update.Message.Chat.ID)
	if connector != nil {
		connectSession.Delete(update.Message.Chat.ID)
		go func() {
			if err := connector.Disconnect(ctx); err != nil {
				log.Printf("Disconnect failed %s", err)
				b.SendMessage(ctx, &bot.SendMessageParams{
					Text:      fmt.Sprintf("Disconnect failed: %v", err),
					ChatID:    update.Message.Chat.ID,
					ParseMode: models.ParseModeHTML,
				})
				return
			}
		}()
	}

	b.SendMessage(ctx, &bot.SendMessageParams{
		Text:      "You have been successfully disconnected!",
		ChatID:    update.Message.Chat.ID,
		ParseMode: models.ParseModeHTML,
	})
}
