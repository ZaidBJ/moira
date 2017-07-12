package mail

import (
	"testing"

	"fmt"
	"github.com/golang/mock/gomock"
	"github.com/moira-alert/moira-alert"
	"github.com/moira-alert/moira-alert/mock/moira-alert"
	. "github.com/smartystreets/goconvey/convey"
	"os"
)

func TestMail(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	logger := mock_moira_alert.NewMockLogger(mockCtrl)

	contact := moira.ContactData{
		ID:    "ContactID-000000000000001",
		Type:  "email",
		Value: "mail1@example.com",
	}

	trigger := moira.TriggerData{
		ID:         "triggerID-0000000000001",
		Name:       "test trigger 1",
		Targets:    []string{"test.target.1"},
		WarnValue:  10,
		ErrorValue: 20,
		Tags:       []string{"test-tag-1"},
	}

	sender := Sender{
		FrontURI: "http://localhost",
		From:     "test@notifier",
		SMTPhost: "localhost",
		SMTPport: 25,
	}
	sender.setLogger(logger)
	events := make([]moira.EventData, 0, 10)
	for event := range generateTestEvents(10, trigger.ID) {
		events = append(events, *event)
	}

	Convey("Make message", t, func() {
		message := sender.makeMessage(events, contact, trigger, true)
		So(message.GetHeader("From")[0], ShouldEqual, sender.From)
		So(message.GetHeader("To")[0], ShouldEqual, contact.Value)
		message.WriteTo(os.Stdout)
	})
}

func generateTestEvents(n int, subscriptionID string) chan *moira.EventData {
	ch := make(chan *moira.EventData)
	go func() {
		for i := 0; i < n; i++ {
			event := &moira.EventData{
				Metric:         fmt.Sprintf("Metric number #%d", i),
				SubscriptionID: subscriptionID,
				State:          "TEST",
			}

			ch <- event
		}
		close(ch)
	}()
	return ch
}