package graph

import pb "github.com/infinimesh/proto/node"

func requestExpiry(expiresAt, legacy int64) int64 {
	if expiresAt != 0 {
		return expiresAt
	}
	return legacy
}

func requestInfinite(req *pb.TokenRequest) bool {
	if req.Infinite != nil {
		return req.GetInfinite()
	}
	return req.GetInf()
}
