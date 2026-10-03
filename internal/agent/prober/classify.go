package prober

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"syscall"
)

// classify 统一连接与发送失败口径：这一次没有联通是可达性事实，计入丢包；本地无法发起才是 error。
func classify(err error) Outcome {
	var timeout net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &timeout) && timeout.Timeout()) {
		return Outcome{Timeout: true}
	}
	for _, remote := range []error{syscall.ECONNREFUSED, syscall.ECONNRESET, syscall.ETIMEDOUT, syscall.ENETUNREACH, syscall.EHOSTUNREACH, syscall.EHOSTDOWN} {
		if errors.Is(err, remote) {
			return Outcome{Timeout: true}
		}
	}
	// TLS 握手失败（含证书错误：过期、名字不符、自签）是"这个端点此刻给不出一次有效握手"，计入丢包（§8）。
	// 列表覆盖带类型的握手失败路径；对端只回 alert 的握手失败没有可判的类型，落在默认的 error。
	var unknownAuthority x509.UnknownAuthorityError
	var hostnameMismatch x509.HostnameError
	var certInvalid x509.CertificateInvalidError
	var certVerify *tls.CertificateVerificationError
	var notTLS tls.RecordHeaderError
	if errors.As(err, &unknownAuthority) || errors.As(err, &hostnameMismatch) || errors.As(err, &certInvalid) || errors.As(err, &certVerify) || errors.As(err, &notTLS) {
		return Outcome{Timeout: true}
	}
	return Outcome{Err: err.Error()}
}
