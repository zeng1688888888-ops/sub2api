package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type intelligenceSettingsAdminStub struct {
	*stubAdminService
	extra map[string]any
}

func (s *intelligenceSettingsAdminStub) CreateAccount(_ context.Context, input *service.CreateAccountInput) (*service.Account, error) {
	s.extra = input.Extra
	return &service.Account{ID: 7, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Extra: input.Extra}, nil
}
func (s *intelligenceSettingsAdminStub) UpdateAccount(_ context.Context, id int64, input *service.UpdateAccountInput) (*service.Account, error) {
	s.extra = input.Extra
	return &service.Account{ID: id, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Extra: input.Extra}, nil
}
func TestAccountIntelligenceSettingCreateAndEditRequests(t *testing.T) {
	for _, method := range []string{http.MethodPost, http.MethodPut} {
		for _, enabled := range []string{"true", "false"} {
			t.Run(method+enabled, func(t *testing.T) {
				gin.SetMode(gin.TestMode)
				svc := &intelligenceSettingsAdminStub{stubAdminService: newStubAdminService()}
				handler := NewAccountHandler(svc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
				router := gin.New()
				router.POST("/accounts/7", handler.Create)
				router.PUT("/accounts/:id", handler.Update)
				body := `{"name":"gpt","platform":"openai","type":"oauth","credentials":{"access_token":"test"},"extra":{"intelligence_auto_pause_enabled":` + enabled + "}}"
				request := httptest.NewRequest(method, "/accounts/7", strings.NewReader(body))
				request.Header.Set("Content-Type", "application/json")
				recorder := httptest.NewRecorder()
				router.ServeHTTP(recorder, request)
				require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
				require.Equal(t, enabled == "true", svc.extra[service.IntelligenceAutoPauseEnabledExtraKey])
			})
		}
	}
}
