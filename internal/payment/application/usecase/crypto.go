package usecase

import (
	"crypto/sha512"
	"encoding/hex"
	"strings"
)

// verifyMidtransSignature: sha512(order_id + status_code + gross_amount + ServerKey).
func verifyMidtransSignature(serverKey, orderID, statusCode, grossAmount, received string) bool {
	raw := orderID + statusCode + grossAmount + serverKey
	h := sha512.New()
	h.Write([]byte(raw))
	expected := hex.EncodeToString(h.Sum(nil))
	return strings.EqualFold(expected, received)
}

// mapTransactionStatus memetakan status Midtrans ke status internal.
func mapTransactionStatus(txStatus, fraudStatus string) string {
	switch txStatus {
	case "capture":
		if fraudStatus == "accept" {
			return "SUCCESS"
		}
		return "FAILED"
	case "settlement":
		return "SUCCESS"
	case "pending":
		return "PENDING"
	case "deny", "cancel", "failure":
		return "FAILED"
	case "expire":
		return "EXPIRED"
	default:
		return "PENDING"
	}
}
