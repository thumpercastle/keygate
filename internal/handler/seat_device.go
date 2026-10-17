package handler

import (
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/tabloy/keygate/internal/service"
	"github.com/tabloy/keygate/pkg/response"
)

// SeatDeviceHandler serves seat-bound desktop sign-in:
//
//	POST /api/v1/license/seat/signin/start     {email, product_id, identifier, label, fingerprint}
//	POST /api/v1/license/seat/signin/poll      {request_id, poll_secret}
//	POST /api/v1/license/seat/signin/describe  {token}   (the emailed page; read-only)
//	POST /api/v1/license/seat/signin/approve   {token}   (the emailed page's button)
//	POST /api/v1/license/seat/verify           {device_secret, identifier, fingerprint}
//	POST /api/v1/license/seat/deactivate       {device_secret, identifier}
type SeatDeviceHandler struct {
	svc *service.SeatDeviceService
}

func NewSeatDeviceHandler(svc *service.SeatDeviceService) *SeatDeviceHandler {
	return &SeatDeviceHandler{svc: svc}
}

func (h *SeatDeviceHandler) Start(c *gin.Context) {
	var req struct {
		Email       string `json:"email" binding:"required"`
		ProductID   string `json:"product_id" binding:"required"`
		Identifier  string `json:"identifier" binding:"required"`
		Label       string `json:"label"`
		Fingerprint string `json:"fingerprint"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "email, product_id and identifier are required")
		return
	}
	res, err := h.svc.Start(c.Request.Context(), service.SigninStartInput{
		Email: req.Email, ProductID: strings.TrimSpace(req.ProductID),
		Identifier: strings.TrimSpace(req.Identifier), Label: req.Label,
		Fingerprint: strings.TrimSpace(req.Fingerprint), IPAddress: c.ClientIP(),
	})
	if err != nil {
		writeAppErr(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	response.OK(c, res)
}

func (h *SeatDeviceHandler) Poll(c *gin.Context) {
	var req struct {
		RequestID  string `json:"request_id" binding:"required"`
		PollSecret string `json:"poll_secret" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "request_id and poll_secret are required")
		return
	}
	res, err := h.svc.Poll(c.Request.Context(), req.RequestID, req.PollSecret)
	if err != nil {
		writeAppErr(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	response.OK(c, res)
}

func (h *SeatDeviceHandler) Describe(c *gin.Context) {
	var req struct {
		Token string `json:"token" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "token is required")
		return
	}
	res, err := h.svc.Describe(c.Request.Context(), req.Token)
	if err != nil {
		writeAppErr(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	response.OK(c, res)
}

func (h *SeatDeviceHandler) Approve(c *gin.Context) {
	var req struct {
		Token string `json:"token" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "token is required")
		return
	}
	res, err := h.svc.Approve(c.Request.Context(), req.Token, c.ClientIP())
	if err != nil {
		writeAppErr(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	response.OK(c, res)
}

type seatDeviceRequest struct {
	DeviceSecret string `json:"device_secret" binding:"required"`
	Identifier   string `json:"identifier" binding:"required"`
	Fingerprint  string `json:"fingerprint"`
}

func (h *SeatDeviceHandler) Verify(c *gin.Context) {
	var req seatDeviceRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "device_secret and identifier are required")
		return
	}
	productID, _ := c.Get("product_id")
	res, err := h.svc.Verify(c.Request.Context(), service.SeatVerifyInput{
		DeviceSecret: req.DeviceSecret, Identifier: strings.TrimSpace(req.Identifier),
		Fingerprint: strings.TrimSpace(req.Fingerprint), ProductID: str(productID),
		IPAddress: c.ClientIP(),
	})
	if err != nil {
		writeAppErr(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	response.OK(c, res)
}

func (h *SeatDeviceHandler) Deactivate(c *gin.Context) {
	var req seatDeviceRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "device_secret and identifier are required")
		return
	}
	productID, _ := c.Get("product_id")
	if err := h.svc.Deactivate(c.Request.Context(), service.SeatVerifyInput{
		DeviceSecret: req.DeviceSecret, Identifier: strings.TrimSpace(req.Identifier),
		ProductID: str(productID), IPAddress: c.ClientIP(),
	}); err != nil {
		writeAppErr(c, err)
		return
	}
	response.OK(c, gin.H{"status": "deactivated"})
}
