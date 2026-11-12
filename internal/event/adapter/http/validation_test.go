package http

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/qldapm-hcmut/ticket-system-uni/internal/event/domain"
)

func TestValidationFieldResponse(t *testing.T) {
	for _, status := range []int{400, 422} {
		rec := httptest.NewRecorder()
		c := echo.New().NewContext(httptest.NewRequest("POST", "/api/v1/events", nil), rec)
		field := &domain.FieldError{Field: "startAt", Message: "Thời gian bắt đầu phải sau thời điểm hiện tại."}
		var err error
		if status == 400 {
			err = failure(c, field)
		} else {
			err = validationFailure(c, status, field)
		}
		if err != nil || rec.Code != status {
			t.Fatalf("status=%d err=%v", rec.Code, err)
		}
		var body struct {
			Error struct {
				Code    string
				Details struct {
					Fields []struct{ Field, Message string }
				}
			}
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body.Error.Code != "VALIDATION_ERROR" || len(body.Error.Details.Fields) != 1 || body.Error.Details.Fields[0].Field != "startAt" || body.Error.Details.Fields[0].Message != field.Message {
			t.Fatal(rec.Body.String())
		}
	}
}
