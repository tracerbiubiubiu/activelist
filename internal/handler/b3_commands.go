package handler

import (
	"io"

	"github.com/gin-gonic/gin"
	"github.com/tracerbiubiubiu/zhuzhao-utils/aksk"
)

func (d *Deps) b3Get(c *gin.Context)    { d.b3Command(c, false, false) }
func (d *Deps) b3Update(c *gin.Context) { d.b3Command(c, true, false) }

func (d *Deps) b3Receipt(c *gin.Context) { d.b3Command(c, true, true) }

func (d *Deps) b3Command(c *gin.Context, write, lookup bool) {
	const maxBody = 256 << 10
	if c.Request.URL.RawQuery != "" || c.Request.URL.ForceQuery {
		BadRequest(c, "unexpected query on ActiveList task")
		return
	}
	raw, err := io.ReadAll(io.LimitReader(c.Request.Body, maxBody+1))
	if err != nil || len(raw) > maxBody {
		BadRequest(c, "invalid ActiveList command body")
		return
	}
	var result map[string]any
	caller := c.GetString(aksk.ContextKeyCaller)
	if lookup {
		result, err = d.Data.B3LookupReceipt(c.Request.Context(), caller, raw)
	} else {
		result, err = d.Data.B3Execute(c.Request.Context(), caller, raw, write)
	}
	if err != nil {
		Fail(c, asAppErr(err))
		return
	}
	OK(c, result)
}
