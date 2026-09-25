package api

import (
	"context"
	"errors"
	"fmt"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	probev1 "github.com/xjetry/probe/gen/probe/v1"
	"github.com/xjetry/probe/internal/hub/auth"
	"github.com/xjetry/probe/internal/hub/store"
)

func apiTokenProto(t store.APIToken) *probev1.ApiToken {
	out := &probev1.ApiToken{Id: t.ID, Name: t.Name, CreatedAt: t.CreatedAt.Unix()}
	if !t.LastUsedAt.IsZero() {
		out.LastUsedAt = proto.Int64(t.LastUsedAt.Unix())
	}
	return out
}

func (s *Service) ListApiTokens(ctx context.Context, _ *connect.Request[probev1.ListApiTokensRequest]) (*connect.Response[probev1.ListApiTokensResponse], error) {
	list, err := s.store.ListAPITokens(ctx)
	if err != nil {
		s.log.Error("listing API tokens failed", "err", err)
		return nil, internalError("listing API tokens failed")
	}
	out := &probev1.ListApiTokensResponse{}
	for _, t := range list {
		out.Tokens = append(out.Tokens, apiTokenProto(t))
	}
	return connect.NewResponse(out), nil
}

func (s *Service) CreateApiToken(ctx context.Context, req *connect.Request[probev1.CreateApiTokenRequest]) (*connect.Response[probev1.CreateApiTokenResponse], error) {
	name, err := cleanName(req.Msg.GetName())
	if err != nil {
		return nil, err
	}
	tok, plain, err := s.auth.CreateAPIToken(ctx, name)
	if errors.Is(err, store.ErrAPITokenLimit) {
		return nil, connect.NewError(connect.CodeResourceExhausted,
			fmt.Errorf("at most %d API tokens may exist; delete an unused one first", auth.MaxAPITokens))
	}
	if err != nil {
		s.log.Error("creating API token failed", "err", err)
		return nil, internalError("creating API token failed")
	}
	return connect.NewResponse(&probev1.CreateApiTokenResponse{ApiToken: apiTokenProto(tok), Token: plain}), nil
}

func (s *Service) DeleteApiToken(ctx context.Context, req *connect.Request[probev1.DeleteApiTokenRequest]) (*connect.Response[probev1.DeleteApiTokenResponse], error) {
	found, err := s.store.DeleteAPIToken(ctx, req.Msg.GetId())
	if err != nil {
		s.log.Error("deleting API token failed", "err", err)
		return nil, internalError("deleting API token failed")
	}
	if !found {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("API token %d does not exist", req.Msg.GetId()))
	}
	return connect.NewResponse(&probev1.DeleteApiTokenResponse{}), nil
}
