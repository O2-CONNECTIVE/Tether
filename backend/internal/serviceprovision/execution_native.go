package serviceprovision

import (
	"context"
	"errors"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/operatorauth"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/serviceready"
	"github.com/O2-CONNECTIVE/rescue-host/backend/servicehost"
)

// nativeExecutionStageは全て元owner threadからだけ呼び出す。
// legacyの強handleとquery-only設定rootは別所有物。previewはquery-onlyだけを
// inner終端後まで保持する。元scopeをSCM threadへ移送しない。
// private seamsはsource/fakeの経路選択検査用。本番は未設定で下記の実関数を使う。
type nativeExecutionCalls struct {
	prepare func(context.Context, Anchor) (*Prepared, error)
	launch  func(context.Context, *Prepared) (*LaunchToken, error)
	guest   func(context.Context, *Prepared) (*GuestConfiguration, error)
	staging func(context.Context, *nativeExecutionStage, servicehost.GuestStagingTicket, servicehost.GuestStagingOwner, *serviceready.InspectionLease) (nativeGuestStager, error)
	runtime func(context.Context, *nativeExecutionStage, servicehost.RuntimeGuestTicket, servicehost.RuntimeGuestOwner, *serviceready.InspectionLease) (nativeRuntimeGuest, error)
	// register replaces only the native supplier registrar; the held Record and
	// policy still come from this stage. Unset uses RegisterHelperSupplier.
	register func(context.Context, Record, operatorauth.SIDPolicy) (supplierRegistration, error)
}
type nativeExecutionStage struct {
	previewSnapshot *servicehost.PreviewDescriptor
	staging         bool
	runtimeGuest    bool
	requestBudgets  servicehost.RequestLoopBudgets
	preview         *PreviewLimits
	calls           nativeExecutionCalls
	cfg             ExecutionConfig
	prepared        *Prepared
	token           *LaunchToken
	guest           *GuestConfiguration
	guestAttempt    *nativeGuestAttempt
	runtimeAttempt  *nativeRuntimeAttempt
	supplier        supplierRegistration
	service         *servicehost.Service
}

func (s *nativeExecutionStage) Initialize(ctx context.Context) (service *servicehost.Service, err error) {
	prepare := s.calls.prepare
	if prepare == nil {
		prepare = PrepareFixed
	}
	s.prepared, err = prepare(ctx, s.cfg.Anchor)
	if err != nil {
		return nil, err
	}
	if s.prepared == nil {
		return nil, ErrRootDenied
	}
	if s.preview != nil {
		if s.staging || s.runtimeGuest {
			guest := s.calls.guest
			if guest == nil {
				guest = func(ctx context.Context, p *Prepared) (*GuestConfiguration, error) {
					if s.runtimeGuest {
						return p.LoadRuntimeGuestConfiguration(ctx)
					}
					return p.LoadFixedGuest(ctx)
				}
			}
			s.guest, err = guest(ctx, s.prepared)
			if err != nil || s.guest == nil || s.guest.runtimeGuest != s.runtimeGuest {
				return nil, errors.Join(ErrRootDenied, err)
			}
			service, snapshot, err := s.composeStagingPreview(ctx)
			if err != nil {
				return nil, err
			}
			s.previewSnapshot = &snapshot
			s.service = service
			return service, nil
		}
		// query-only rootを借用する。強token/Approval/Launcherは取得しない。
		service, snapshot, err := s.prepared.composePreviewSnapshot(ctx, *s.preview)
		if err != nil {
			return nil, err
		}
		s.previewSnapshot = &snapshot
		s.service = service
		return service, nil
	}
	// Full構成は固定guest recordを必須とし、未配置なら強取得前に閉じる。
	// 読取は正規承認/grantの発行やguestの有効化を行わない。
	guest := s.calls.guest
	if guest == nil {
		guest = func(ctx context.Context, p *Prepared) (*GuestConfiguration, error) { return p.LoadFixedGuest(ctx) }
	}
	s.guest, err = guest(ctx, s.prepared)
	if err != nil {
		return nil, err
	}
	if s.guest == nil {
		return nil, ErrRootDenied
	}
	launch := s.calls.launch
	if launch == nil {
		launch = prepareExecutionToken
	}
	s.token, err = launch(ctx, s.prepared)
	if err != nil {
		return nil, err
	}
	if s.token == nil {
		return nil, ErrRootDenied
	}
	strong, err := s.token.Borrow(ctx)
	if err != nil {
		return nil, err
	}
	return s.prepared.ComposeService(ctx, strong, s.cfg.Approval)
}

func prepareExecutionToken(ctx context.Context, prepared *Prepared) (*LaunchToken, error) {
	query, err := prepared.loaded.QueryToken()
	if err != nil {
		return nil, err
	}
	r := prepared.record
	factory, err := NewNativeLaunchFactory(serviceready.Config{Profile: r.Profile, PrimaryToken: query, Endpoint: r.Endpoint, ServiceCommandLine: r.ServiceCommandLine, ServiceSecurity: r.ServiceSecurity, ProcessSecurity: r.ProcessSecurity, CleanupTimeout: r.ReadyCleanupTimeout})
	if err != nil {
		return nil, err
	}
	return factory.Prepare(ctx)
}

func (s *nativeExecutionStage) Close(ctx context.Context) error {
	if (s.guestAttempt != nil && !s.guestAttempt.closed) || (s.runtimeAttempt != nil && !s.runtimeAttempt.closed) {
		return ErrRootUnconfirmed
	}
	// Close runs only after the inner cleanup proof (or before publication), so
	// every Batch has confirmed its generation; only then is the registration removed.
	if s.supplier != nil {
		if err := s.supplier.Close(ctx); err != nil {
			if errors.Is(err, operatorauth.ErrFatal) {
				return errors.Join(ErrRootFatal, err)
			}
			return errors.Join(ErrRootUnconfirmed, err)
		}
		s.supplier = nil
	}
	if s.guest != nil {
		if err := s.guest.Close(ctx); err != nil {
			return err
		}
		s.guest = nil
	}
	if s.token != nil {
		if err := s.token.Close(ctx); err != nil {
			return err
		}
		s.token = nil
	}
	if s.prepared != nil {
		if err := s.prepared.Close(ctx); err != nil {
			return err
		}
		s.prepared = nil
	}
	return nil
}
