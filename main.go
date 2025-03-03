package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/gorilla/websocket"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
)

var (
	rdb *redis.Client
	ctx = context.Background()
)

type Notification struct {
	ID        string    `json:"id"`
	Message   string    `json:"message"`
	Verified  bool      `json:"verified"`
	Timestamp time.Time `json:"timestamp"`
}

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

func main() {
	rdb = redis.NewClient(&redis.Options{
		Addr:     "localhost:6379",
		Password: "",
		DB:       0,
	})

	e := echo.New()
	e.Use(middleware.Logger())
	e.Use(middleware.Recover())

	e.GET("/ws", handleWebSocket)
	e.POST("/notifications", createNotification)
	e.PUT("/notifications/verify/:id", verifyNotification)
	e.GET("/notifications/unverified", getUnverifiedNotifications)
	e.GET("/notifications/verified", getVerifiedNotifications)

	log.Println("Server running on :8080")
	e.Logger.Fatal(e.Start(":8080"))
}

func handleWebSocket(c echo.Context) error {
	ws, err := upgrader.Upgrade(c.Response(), c.Request(), nil)
	if err != nil {
		log.Println("WebSocket upgrade error:", err)
		return err
	}
	defer ws.Close()

	pubsub := rdb.Subscribe(ctx, "notifications:new", "notifications:verified")
	defer pubsub.Close()

	msgCh := make(chan *redis.Message)
	go func() {
		for msg := range msgCh {
			ws.WriteJSON(map[string]interface{}{
				"channel": msg.Channel,
				"payload": msg.Payload,
			})
		}
	}()

	for {
		msg, err := pubsub.ReceiveMessage(ctx)
		if err != nil {
			log.Println("Redis pubsub error:", err)
			break
		}
		msgCh <- msg
	}
	close(msgCh)

	return nil
}

func createNotification(c echo.Context) error {
	var notification Notification
	if err := c.Bind(&notification); err != nil {
		return c.JSON(http.StatusBadRequest, err.Error())
	}

	notification.ID = fmt.Sprintf("%d", time.Now().UnixNano())
	notification.Timestamp = time.Now()
	notification.Verified = false

	notificationJSON, err := json.Marshal(notification)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, "Error encoding notification")
	}

	if err := rdb.LPush(ctx, "notifications:unverified", notificationJSON).Err(); err != nil {
		return c.JSON(http.StatusInternalServerError, "Error saving notification")
	}
	rdb.Publish(ctx, "notifications:new", notificationJSON)

	return c.JSON(http.StatusOK, notification)
}

func verifyNotification(c echo.Context) error {
	id := c.Param("id")

	notifications, err := rdb.LRange(ctx, "notifications:unverified", 0, -1).Result()
	if err != nil {
		return c.JSON(http.StatusInternalServerError, "Error retrieving notifications")
	}

	for _, n := range notifications {
		var notification Notification
		if err := json.Unmarshal([]byte(n), &notification); err != nil {
			continue
		}

		if notification.ID == id {
			if err := rdb.LRem(ctx, "notifications:unverified", 1, n).Err(); err != nil {
				return c.JSON(http.StatusInternalServerError, "Error removing notification")
			}

			notification.Verified = true
			updatedJSON, _ := json.Marshal(notification)
			if err := rdb.RPush(ctx, "notifications:verified", updatedJSON).Err(); err != nil {
				return c.JSON(http.StatusInternalServerError, "Error updating notification")
			}

			rdb.Publish(ctx, "notifications:verified", updatedJSON)
			return c.NoContent(http.StatusOK)
		}
	}

	return c.JSON(http.StatusNotFound, "Notification not found")
}

func getUnverifiedNotifications(c echo.Context) error {
	notifications, err := rdb.LRange(ctx, "notifications:unverified", 0, -1).Result()
	if err != nil {
		return c.JSON(http.StatusInternalServerError, "Error retrieving notifications")
	}
	return sendNotifications(c, notifications)
}

func getVerifiedNotifications(c echo.Context) error {
	notifications, err := rdb.LRange(ctx, "notifications:verified", 0, -1).Result()
	if err != nil {
		return c.JSON(http.StatusInternalServerError, "Error retrieving notifications")
	}
	return sendNotifications(c, notifications)
}

func sendNotifications(c echo.Context, notifications []string) error {
	var result []Notification
	for _, n := range notifications {
		var notification Notification
		if err := json.Unmarshal([]byte(n), &notification); err != nil {
			continue
		}
		result = append(result, notification)
	}
	return c.JSON(http.StatusOK, result)
}
