package core

import (
	"context"
	"github.com/anytypeio/any-sync/app"
	"os"
	"path/filepath"

	"github.com/anytypeio/go-anytype-middleware/core/anytype"
	"github.com/anytypeio/go-anytype-middleware/core/anytype/config"
	"github.com/anytypeio/go-anytype-middleware/core/block"
	importer "github.com/anytypeio/go-anytype-middleware/core/block/import"
	"github.com/anytypeio/go-anytype-middleware/metrics"
	"github.com/anytypeio/go-anytype-middleware/pb"
	"github.com/anytypeio/go-anytype-middleware/pkg/lib/core"
	"github.com/anytypeio/go-anytype-middleware/pkg/lib/pb/model"
	"github.com/anytypeio/go-anytype-middleware/pkg/lib/wallet"
	"github.com/anytypeio/go-anytype-middleware/util/files"
	"github.com/anytypeio/go-anytype-middleware/util/pbtypes"
)

func (mw *Middleware) UserDataImport(cctx context.Context,
	req *pb.RpcUserDataImportRequest) *pb.RpcUserDataImportResponse {
	ctx := mw.newContext(cctx)

	response := func(code pb.RpcUserDataImportResponseErrorCode, err error) *pb.RpcUserDataImportResponse {
		m := &pb.RpcUserDataImportResponse{Error: &pb.RpcUserDataImportResponseError{Code: code}}
		if err != nil {
			m.Error.Description = err.Error()
		}
		return m
	}

	profile, err := importer.ImportUserProfile(ctx, req)
	if err != nil {
		return response(pb.RpcUserDataImportResponseError_UNKNOWN_ERROR, err)
	}
	err = mw.createAccount(profile, req)
	if err != nil {
		return response(pb.RpcUserDataImportResponseError_UNKNOWN_ERROR, err)
	}

	importer := mw.app.MustComponent(importer.CName).(importer.Importer)
	err = importer.ImportUserData(ctx, req)

	if err != nil {
		return response(pb.RpcUserDataImportResponseError_UNKNOWN_ERROR, err)
	}

	mw.mnemonic = ""

	return response(pb.RpcUserDataImportResponseError_NULL, nil)
}

func (mw *Middleware) createAccount(profile *pb.Profile, req *pb.RpcUserDataImportRequest) error {
	mw.m.Lock()

	defer mw.m.Unlock()
	if err := mw.stop(); err != nil {
		return err
	}

	mw.rootPath = req.RootPath
	mw.foundAccounts = nil

	err := os.MkdirAll(mw.rootPath, 0700)
	if err != nil {
		return err
	}
	err = mw.setMnemonic(profile.Mnemonic)
	if err != nil {
		return err
	}
	mw.accountSearchCancel()

	cfg := anytype.BootstrapConfig(true, os.Getenv("ANYTYPE_STAGING") == "1", false)
	index := len(mw.foundAccounts)
	var account wallet.Keypair
	account, err = core.WalletAccountAt(mw.mnemonic, index, "")
	if err != nil {
		return err
	}

	if req.StorePath != "" && req.StorePath != mw.rootPath {
		configPath := filepath.Join(mw.rootPath, account.Address(), config.ConfigFileName)

		storePath := filepath.Join(req.StorePath, account.Address())

		err = os.MkdirAll(storePath, 0700)
		if err != nil {
			return err
		}

		if err = files.WriteJsonConfig(configPath, config.ConfigRequired{IPFSStorageAddr: storePath}); err != nil {
			return err
		}
	}

	newAcc := &model.Account{Id: account.Address()}

	comps := []app.Component{
		cfg,
		anytype.BootstrapWallet(mw.rootPath, account.Address()),
		mw.EventSender,
	}

	ctxWithValue := context.WithValue(context.Background(), metrics.CtxKeyRequest, "account_create")
	if mw.app, err = anytype.StartNewApp(ctxWithValue, comps...); err != nil {
		return err
	}

	newAcc.Name = profile.Name
	details := []*pb.RpcObjectSetDetailsDetail{{Key: "name", Value: pbtypes.String(profile.Name)}}
	newAcc.Avatar = &model.AccountAvatar{Avatar: &model.AccountAvatarAvatarOfImage{
		Image: &model.BlockContentFile{Hash: profile.Avatar},
	}}
	details = append(details, &pb.RpcObjectSetDetailsDetail{
		Key:   "iconImage",
		Value: pbtypes.String(profile.Avatar),
	})

	newAcc.Info = mw.getInfo()
	bs := mw.app.MustComponent(block.CName).(*block.Service)
	coreService := mw.app.MustComponent(core.CName).(core.Service)
	if err = bs.SetDetails(nil, pb.RpcObjectSetDetailsRequest{
		ContextId: coreService.PredefinedBlocks().Profile,
		Details:   details,
	}); err != nil {
		return err
	}

	mw.foundAccounts = append(mw.foundAccounts, newAcc)
	return nil
}