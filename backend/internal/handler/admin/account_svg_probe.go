package admin

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

func (h *AccountHandler) GetSVGProbeSettings(c *gin.Context) {
	v, err := h.accountTestService.GetAccountSVGProbeSettings(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, v)
}
func (h *AccountHandler) SaveSVGProbeSettings(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 512<<10)
	var v service.AccountSVGProbeSettings
	if err := c.ShouldBindJSON(&v); err != nil {
		response.BadRequest(c, "Invalid SVG probe settings")
		return
	}
	if err := h.accountTestService.SaveAccountSVGProbeSettings(c.Request.Context(), &v); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, v)
}
func (h *AccountHandler) SVGProbe(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "Invalid account ID")
		return
	}
	result, err := h.accountTestService.ProbeAccountSVG(c.Request.Context(), id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}
func (h *AccountHandler) SVGProbeImage(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "Invalid account ID")
		return
	}
	data, mime, err := h.accountTestService.ReadAccountSVGProbe(c.Request.Context(), id, c.Param("result_id"))
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	c.Header("Cache-Control", "private, no-store")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Content-Security-Policy", service.AccountProbeDocumentCSP+"; sandbox")
	extension := "svg"
	if mime == "text/html" {
		extension = "html"
	}
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=account-%d-pelican-test.%s", id, extension))
	c.Data(http.StatusOK, mime, data)
}
