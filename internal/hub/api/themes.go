package api

import (
	"context"
	"errors"
	"fmt"

	"connectrpc.com/connect"

	probev1 "github.com/xjetry/probe/gen/probe/v1"
	"github.com/xjetry/probe/internal/hub/store"
	"github.com/xjetry/probe/internal/hub/theme"
)

// requireThemeOrigin 是五个主题方法的第一道判定。没有独立的主题 origin 时整体关闭，而不是"能传但不生效"：与面板
// 同源的主题脚本能直接 fetch AdminService，浏览器会自动附带管理员的会话 cookie，§5.3 的几条 CSRF 事实挡的是跨站请求，
// 对同源脚本一条都不成立；能上传会让人以为差一步启用，而实际差的是一个域名。
func (s *Service) requireThemeOrigin() error {
	if s.cfg.ThemeOrigin {
		return nil
	}
	return connect.NewError(connect.CodeFailedPrecondition, errors.New(
		"themes are disabled because this hub has no theme origin: a theme's scripts on the panel's origin could call the admin API "+
			"with a signed-in administrator's session, so themes are served only from a separate hostname; point a second hostname "+
			"(for example status.example.com) at the hub and start probe-hub serve with --theme-origin https://<that hostname>"))
}

func themeProto(t store.Theme) *probev1.Theme {
	return &probev1.Theme{Id: t.ID, Name: t.Name, Version: t.Version, UploadedAt: t.UploadedAt.Unix(), Enabled: t.Enabled, HasPreview: t.Preview != ""}
}

func themeNotFound(field, id string) error {
	return connect.NewError(connect.CodeNotFound, fmt.Errorf("%s: theme %q is not installed", field, id))
}

func (s *Service) UploadTheme(ctx context.Context, req *connect.Request[probev1.UploadThemeRequest]) (*connect.Response[probev1.UploadThemeResponse], error) {
	if err := s.requireThemeOrigin(); err != nil {
		return nil, err
	}
	select {
	case s.uploading <- struct{}{}:
		defer func() { <-s.uploading }()
	default:
		return nil, connect.NewError(connect.CodeResourceExhausted, errors.New("another theme upload is in progress; retry after it finishes"))
	}
	pkg, err := theme.Parse(req.Msg.GetPackage())
	var bad *theme.Error
	if errors.As(err, &bad) {
		// bad 自带位置：package（整个包）、entry "<路径>"（某个条目）或 theme.json <字段>（清单）。
		return nil, invalid("%s", bad)
	}
	if err != nil {
		s.log.Error("parsing theme package failed", "err", err)
		return nil, internalError("parsing theme package failed")
	}
	id := pkg.Manifest.ID
	expect := req.Msg.GetExpectId()
	if expect != "" && expect != id {
		return nil, invalid("expect_id: %q does not match the package's theme.json id %q; this package is a different theme", expect, id)
	}
	files := make([]store.ThemeFile, len(pkg.Files))
	for i, f := range pkg.Files {
		files[i] = store.ThemeFile{Path: f.Path, Content: f.Content}
	}
	meta := store.Theme{ID: id, Name: pkg.Manifest.Name, Version: pkg.Manifest.Version, Preview: pkg.Manifest.Preview, UploadedAt: s.clk.Now()}
	got, err := s.store.PutTheme(ctx, meta, files, expect != "", theme.MaxThemes)
	switch {
	case errors.Is(err, store.ErrThemeLimit):
		return nil, connect.NewError(connect.CodeResourceExhausted,
			fmt.Errorf("at most %d themes may be installed; delete one first (uploading an installed id replaces it and does not count)", theme.MaxThemes))
	case errors.Is(err, store.ErrNotFound):
		return nil, connect.NewError(connect.CodeNotFound,
			fmt.Errorf("expect_id: theme %q is not installed; omit expect_id to install it as a new theme", expect))
	case err != nil:
		s.log.Error("storing theme failed", "err", err, "theme", id)
		return nil, internalError("storing theme failed")
	}
	s.log.Info("theme uploaded", "theme", id, "version", got.Version, "files", len(files))
	return connect.NewResponse(&probev1.UploadThemeResponse{Theme: themeProto(got)}), nil
}

func (s *Service) ListThemes(ctx context.Context, _ *connect.Request[probev1.ListThemesRequest]) (*connect.Response[probev1.ListThemesResponse], error) {
	if err := s.requireThemeOrigin(); err != nil {
		return nil, err
	}
	list, err := s.store.ListThemes(ctx)
	if err != nil {
		s.log.Error("listing themes failed", "err", err)
		return nil, internalError("listing themes failed")
	}
	out := &probev1.ListThemesResponse{Themes: make([]*probev1.Theme, 0, len(list))}
	for _, t := range list {
		out.Themes = append(out.Themes, themeProto(t))
	}
	return connect.NewResponse(out), nil
}

func (s *Service) EnableTheme(ctx context.Context, req *connect.Request[probev1.EnableThemeRequest]) (*connect.Response[probev1.EnableThemeResponse], error) {
	if err := s.requireThemeOrigin(); err != nil {
		return nil, err
	}
	id := req.Msg.GetId()
	err := s.store.EnableTheme(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, themeNotFound("id", id)
	}
	if err != nil {
		s.log.Error("enabling theme failed", "err", err, "theme", id)
		return nil, internalError("enabling theme failed")
	}
	s.log.Info("theme enabled", "theme", id)
	return connect.NewResponse(&probev1.EnableThemeResponse{}), nil
}

func (s *Service) DeleteTheme(ctx context.Context, req *connect.Request[probev1.DeleteThemeRequest]) (*connect.Response[probev1.DeleteThemeResponse], error) {
	if err := s.requireThemeOrigin(); err != nil {
		return nil, err
	}
	id := req.Msg.GetId()
	err := s.store.DeleteTheme(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, themeNotFound("id", id)
	}
	if err != nil {
		s.log.Error("deleting theme failed", "err", err, "theme", id)
		return nil, internalError("deleting theme failed")
	}
	s.log.Info("theme deleted", "theme", id)
	return connect.NewResponse(&probev1.DeleteThemeResponse{}), nil
}

func (s *Service) GetThemePreview(ctx context.Context, req *connect.Request[probev1.GetThemePreviewRequest]) (*connect.Response[probev1.GetThemePreviewResponse], error) {
	if err := s.requireThemeOrigin(); err != nil {
		return nil, err
	}
	id := req.Msg.GetId()
	t, content, err := s.store.ThemePreview(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, themeNotFound("id", id)
	}
	if err != nil {
		s.log.Error("reading theme preview failed", "err", err, "theme", id)
		return nil, internalError("reading theme preview failed")
	}
	if t.Preview == "" {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("id: theme %q has no preview in its theme.json", id))
	}
	return connect.NewResponse(&probev1.GetThemePreviewResponse{Content: content, ContentType: theme.PreviewContentType(t.Preview)}), nil
}
