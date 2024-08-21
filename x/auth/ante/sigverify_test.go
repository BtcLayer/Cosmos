package ante_test

import (
	"fmt"
	"testing"

	secp256k1dcrd "github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/require"

	bankv1beta1 "cosmossdk.io/api/cosmos/bank/v1beta1"
	storetypes "cosmossdk.io/store/types"
	"cosmossdk.io/x/auth/ante"
	"cosmossdk.io/x/auth/migrations/legacytx"
	authsign "cosmossdk.io/x/auth/signing"
	authtx "cosmossdk.io/x/auth/tx"
	txmodule "cosmossdk.io/x/auth/tx/config"
	"cosmossdk.io/x/auth/types"

	"github.com/cosmos/cosmos-sdk/codec"
	"github.com/cosmos/cosmos-sdk/crypto/keys/ed25519"
	kmultisig "github.com/cosmos/cosmos-sdk/crypto/keys/multisig"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256r1"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	"github.com/cosmos/cosmos-sdk/crypto/types/multisig"
	"github.com/cosmos/cosmos-sdk/testutil/testdata"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/tx/signing"
)

func TestSetPubKey(t *testing.T) {
	suite := SetupTestSuite(t, true)
	suite.txBuilder = suite.clientCtx.TxConfig.NewTxBuilder()

	// keys and addresses
	priv1, pub1, addr1 := testdata.KeyTestPubAddr()
	priv2, pub2, addr2 := testdata.KeyTestPubAddr()
	priv3, pub3, addr3 := testdata.KeyTestPubAddrSecp256R1(t)

	addrs := []sdk.AccAddress{addr1, addr2, addr3}
	pubs := []cryptotypes.PubKey{pub1, pub2, pub3}

	msgs := make([]sdk.Msg, len(addrs))
	// set accounts and create msg for each address
	for i, addr := range addrs {
		acc := suite.accountKeeper.NewAccountWithAddress(suite.ctx, addr)
		require.NoError(t, acc.SetAccountNumber(uint64(i+1000)))
		suite.accountKeeper.SetAccount(suite.ctx, acc)
		msgs[i] = testdata.NewTestMsg(addr)
	}
	require.NoError(t, suite.txBuilder.SetMsgs(msgs...))
	suite.txBuilder.SetFeeAmount(testdata.NewTestFeeAmount())
	suite.txBuilder.SetGasLimit(testdata.NewTestGasLimit())

	privs, accNums, accSeqs := []cryptotypes.PrivKey{priv1, priv2, priv3}, []uint64{0, 1, 2}, []uint64{0, 0, 0}
	tx, err := suite.CreateTestTx(suite.ctx, privs, accNums, accSeqs, suite.ctx.ChainID(), signing.SignMode_SIGN_MODE_DIRECT)
	require.NoError(t, err)

	spkd := ante.NewSetPubKeyDecorator(suite.accountKeeper)
	antehandler := sdk.ChainAnteDecorators(spkd)

	ctx, err := antehandler(suite.ctx, tx, false)
	require.NoError(t, err)

	// Require that all accounts have pubkey set after Decorator runs
	for i, addr := range addrs {
		pk, err := suite.accountKeeper.GetPubKey(ctx, addr)
		require.NoError(t, err, "Error on retrieving pubkey from account")
		require.True(t, pubs[i].Equals(pk),
			"Wrong Pubkey retrieved from AccountKeeper, idx=%d\nexpected=%s\n     got=%s", i, pubs[i], pk)
	}
}

func TestConsumeSignatureVerificationGas(t *testing.T) {
	suite := SetupTestSuite(t, true)
	params := types.DefaultParams()
	msg := []byte{1, 2, 3, 4}

	p := types.DefaultParams()
	skR1, _ := secp256r1.GenPrivKey()
	pkSet1, sigSet1 := generatePubKeysAndSignatures(5, msg, false)
	multisigKey1 := kmultisig.NewLegacyAminoPubKey(2, pkSet1)
	multisignature1 := multisig.NewMultisig(len(pkSet1))
	expectedCost1 := expectedGasCostByKeys(pkSet1)
	for i := 0; i < len(pkSet1); i++ {
		stdSig := legacytx.StdSignature{PubKey: pkSet1[i], Signature: sigSet1[i]} //nolint:staticcheck // SA1019: legacytx.StdSignature is deprecated
		sigV2, err := legacytx.StdSignatureToSignatureV2(suite.clientCtx.LegacyAmino, stdSig)
		require.NoError(t, err)
		err = multisig.AddSignatureV2(multisignature1, sigV2, pkSet1)
		require.NoError(t, err)
	}

	type args struct {
		meter  storetypes.GasMeter
		sig    signing.SignatureData
		pubkey cryptotypes.PubKey
		params types.Params
	}
	tests := []struct {
		name        string
		args        args
		gasConsumed uint64
		shouldErr   bool
	}{
		{"PubKeyEd25519", args{storetypes.NewInfiniteGasMeter(), nil, ed25519.GenPrivKey().PubKey(), params}, p.SigVerifyCostED25519, true},
		{"PubKeySecp256k1", args{storetypes.NewInfiniteGasMeter(), nil, secp256k1.GenPrivKey().PubKey(), params}, p.SigVerifyCostSecp256k1, false},
		{"PubKeySecp256r1", args{storetypes.NewInfiniteGasMeter(), nil, skR1.PubKey(), params}, p.SigVerifyCostSecp256r1(), false},
		{"Multisig", args{storetypes.NewInfiniteGasMeter(), multisignature1, multisigKey1, params}, expectedCost1, false},
		{"unknown key", args{storetypes.NewInfiniteGasMeter(), nil, nil, params}, 0, true},
	}
	for _, tt := range tests {
		sigV2 := signing.SignatureV2{
			PubKey:   tt.args.pubkey,
			Data:     tt.args.sig,
			Sequence: 0, // Arbitrary account sequence
		}
		err := ante.DefaultSigVerificationGasConsumer(tt.args.meter, sigV2, tt.args.params)

		if tt.shouldErr {
			require.NotNil(t, err)
		} else {
			require.Nil(t, err)
			require.Equal(t, tt.gasConsumed, tt.args.meter.GasConsumed(), fmt.Sprintf("%d != %d", tt.gasConsumed, tt.args.meter.GasConsumed()))
		}
	}
}

func TestSigVerification(t *testing.T) {
	suite := SetupTestSuite(t, true)
	suite.txBankKeeper.EXPECT().DenomMetadataV2(gomock.Any(), gomock.Any()).Return(&bankv1beta1.QueryDenomMetadataResponse{}, nil).AnyTimes()

	enabledSignModes := []signing.SignMode{signing.SignMode_SIGN_MODE_DIRECT, signing.SignMode_SIGN_MODE_TEXTUAL, signing.SignMode_SIGN_MODE_LEGACY_AMINO_JSON}
	// Since TEXTUAL is not enabled by default, we create a custom TxConfig
	// here which includes it.
	txConfigOpts := authtx.ConfigOptions{
		TextualCoinMetadataQueryFn: txmodule.NewGRPCCoinMetadataQueryFn(suite.clientCtx),
		EnabledSignModes:           enabledSignModes,
	}
	var err error
	suite.clientCtx.TxConfig, err = authtx.NewTxConfigWithOptions(
		codec.NewProtoCodec(suite.encCfg.InterfaceRegistry),
		txConfigOpts,
	)
	require.NoError(t, err)
	suite.txBuilder = suite.clientCtx.TxConfig.NewTxBuilder()

	// make block height non-zero to ensure account numbers part of signBytes
	suite.ctx = suite.ctx.WithBlockHeight(1)

	// keys and addresses
	priv1, _, addr1 := testdata.KeyTestPubAddr()
	priv2, _, addr2 := testdata.KeyTestPubAddr()
	priv3, _, addr3 := testdata.KeyTestPubAddr()

	addrs := []sdk.AccAddress{addr1, addr2, addr3}

	msgs := make([]sdk.Msg, len(addrs))
	accs := make([]sdk.AccountI, len(addrs))
	// set accounts and create msg for each address
	for i, addr := range addrs {
		acc := suite.accountKeeper.NewAccountWithAddress(suite.ctx, addr)
		require.NoError(t, acc.SetAccountNumber(uint64(i)+1000))
		suite.accountKeeper.SetAccount(suite.ctx, acc)
		msgs[i] = testdata.NewTestMsg(addr)
		accs[i] = acc
	}

	feeAmount := testdata.NewTestFeeAmount()
	gasLimit := testdata.NewTestGasLimit()

	spkd := ante.NewSetPubKeyDecorator(suite.accountKeeper)
	txConfigOpts = authtx.ConfigOptions{
		TextualCoinMetadataQueryFn: txmodule.NewBankKeeperCoinMetadataQueryFn(suite.txBankKeeper),
		EnabledSignModes:           enabledSignModes,
	}
	anteTxConfig, err := authtx.NewTxConfigWithOptions(
		codec.NewProtoCodec(suite.encCfg.InterfaceRegistry),
		txConfigOpts,
	)
	require.NoError(t, err)
	svd := ante.NewSigVerificationDecorator(suite.accountKeeper, anteTxConfig.SignModeHandler())
	antehandler := sdk.ChainAnteDecorators(spkd, svd)
	defaultSignMode, err := authsign.APISignModeToInternal(anteTxConfig.SignModeHandler().DefaultMode())
	require.NoError(t, err)

	type testCase struct {
		name        string
		privs       []cryptotypes.PrivKey
		accNums     []uint64
		accSeqs     []uint64
		invalidSigs bool // used for testing sigverify on RecheckTx
		recheck     bool
		sigverify   bool
		shouldErr   bool
	}
	validSigs := false
	testCases := []testCase{
		{"no signers", []cryptotypes.PrivKey{}, []uint64{}, []uint64{}, validSigs, false, true, true},
		{"not enough signers", []cryptotypes.PrivKey{priv1, priv2}, []uint64{accs[0].GetAccountNumber(), accs[1].GetAccountNumber()}, []uint64{0, 0}, validSigs, false, true, true},
		{"wrong order signers", []cryptotypes.PrivKey{priv3, priv2, priv1}, []uint64{accs[2].GetAccountNumber(), accs[1].GetAccountNumber(), accs[0].GetAccountNumber()}, []uint64{0, 0, 0}, validSigs, false, true, true},
		{"wrong accnums", []cryptotypes.PrivKey{priv1, priv2, priv3}, []uint64{7, 8, 9}, []uint64{0, 0, 0}, validSigs, false, true, true},
		{"wrong sequences", []cryptotypes.PrivKey{priv1, priv2, priv3}, []uint64{accs[0].GetAccountNumber(), accs[1].GetAccountNumber(), accs[2].GetAccountNumber()}, []uint64{3, 4, 5}, validSigs, false, true, true},
		{"valid tx", []cryptotypes.PrivKey{priv1, priv2, priv3}, []uint64{accs[0].GetAccountNumber(), accs[1].GetAccountNumber(), accs[2].GetAccountNumber()}, []uint64{0, 0, 0}, validSigs, false, true, false},
		{"sigverify tx with wrong order signers", []cryptotypes.PrivKey{priv3, priv2, priv1}, []uint64{accs[0].GetAccountNumber(), accs[1].GetAccountNumber(), accs[2].GetAccountNumber()}, []uint64{0, 0, 0}, validSigs, false, true, true},
		{"skip sigverify tx with wrong order signers", []cryptotypes.PrivKey{priv3, priv2, priv1}, []uint64{accs[0].GetAccountNumber(), accs[1].GetAccountNumber(), accs[2].GetAccountNumber()}, []uint64{0, 0, 0}, validSigs, false, false, false},
		{"no err on recheck", []cryptotypes.PrivKey{priv1, priv2, priv3}, []uint64{0, 0, 0}, []uint64{0, 0, 0}, !validSigs, true, true, false},
	}

	for i, tc := range testCases {
		for _, signMode := range enabledSignModes {
			t.Run(fmt.Sprintf("%s with %s", tc.name, signMode), func(t *testing.T) {
				suite.ctx = suite.ctx.WithIsReCheckTx(tc.recheck).WithIsSigverifyTx(tc.sigverify)
				suite.txBuilder = suite.clientCtx.TxConfig.NewTxBuilder() // Create new txBuilder for each test

				require.NoError(t, suite.txBuilder.SetMsgs(msgs...))
				suite.txBuilder.SetFeeAmount(feeAmount)
				suite.txBuilder.SetGasLimit(gasLimit)

				tx, err := suite.CreateTestTx(suite.ctx, tc.privs, tc.accNums, tc.accSeqs, suite.ctx.ChainID(), signMode)
				require.NoError(t, err)
				if tc.invalidSigs {
					txSigs, _ := tx.GetSignaturesV2()
					badSig, _ := tc.privs[0].Sign([]byte("unrelated message"))
					txSigs[0] = signing.SignatureV2{
						PubKey: tc.privs[0].PubKey(),
						Data: &signing.SingleSignatureData{
							SignMode:  defaultSignMode,
							Signature: badSig,
						},
						Sequence: tc.accSeqs[0],
					}
					err := suite.txBuilder.SetSignatures(txSigs...)
					require.NoError(t, err)

					tx = suite.txBuilder.GetTx()
				}

				txBytes, err := suite.clientCtx.TxConfig.TxEncoder()(tx)
				require.NoError(t, err)
				byteCtx := suite.ctx.WithTxBytes(txBytes)
				_, err = antehandler(byteCtx, tx, false)
				if tc.shouldErr {
					require.NotNil(t, err, "TestCase %d: %s did not error as expected", i, tc.name)
				} else {
					require.Nil(t, err, "TestCase %d: %s errored unexpectedly. Err: %v", i, tc.name, err)
				}
			})
		}
	}
}

func TestSigIntegration(t *testing.T) {
	// generate private keys
	privs := []cryptotypes.PrivKey{
		secp256k1.GenPrivKey(),
		secp256k1.GenPrivKey(),
		secp256k1.GenPrivKey(),
	}

	params := types.DefaultParams()
	initialSigCost := params.SigVerifyCostSecp256k1
	initialCost, err := runSigDecorators(t, params, false, privs...)
	require.Nil(t, err)

	params.SigVerifyCostSecp256k1 *= 2
	doubleCost, err := runSigDecorators(t, params, false, privs...)
	require.Nil(t, err)

	require.Equal(t, initialSigCost*uint64(len(privs)), doubleCost-initialCost)
}

func runSigDecorators(t *testing.T, params types.Params, _ bool, privs ...cryptotypes.PrivKey) (storetypes.Gas, error) {
	t.Helper()
	suite := SetupTestSuite(t, true)
	suite.txBuilder = suite.clientCtx.TxConfig.NewTxBuilder()

	// Make block-height non-zero to include accNum in SignBytes
	suite.ctx = suite.ctx.WithBlockHeight(1)
	err := suite.accountKeeper.Params.Set(suite.ctx, params)
	require.NoError(t, err)

	msgs := make([]sdk.Msg, len(privs))
	accNums := make([]uint64, len(privs))
	accSeqs := make([]uint64, len(privs))
	// set accounts and create msg for each address
	for i, priv := range privs {
		addr := sdk.AccAddress(priv.PubKey().Address())
		acc := suite.accountKeeper.NewAccountWithAddress(suite.ctx, addr)
		require.NoError(t, acc.SetAccountNumber(uint64(i)+1000))
		suite.accountKeeper.SetAccount(suite.ctx, acc)
		msgs[i] = testdata.NewTestMsg(addr)
		accNums[i] = acc.GetAccountNumber()
		accSeqs[i] = uint64(0)
	}
	require.NoError(t, suite.txBuilder.SetMsgs(msgs...))

	feeAmount := testdata.NewTestFeeAmount()
	gasLimit := testdata.NewTestGasLimit()
	suite.txBuilder.SetFeeAmount(feeAmount)
	suite.txBuilder.SetGasLimit(gasLimit)

	tx, err := suite.CreateTestTx(suite.ctx, privs, accNums, accSeqs, suite.ctx.ChainID(), signing.SignMode_SIGN_MODE_DIRECT)
	require.NoError(t, err)

	spkd := ante.NewSetPubKeyDecorator(suite.accountKeeper)
	svgc := ante.NewSigGasConsumeDecorator(suite.accountKeeper, ante.DefaultSigVerificationGasConsumer)
	svd := ante.NewSigVerificationDecorator(suite.accountKeeper, suite.clientCtx.TxConfig.SignModeHandler())
	antehandler := sdk.ChainAnteDecorators(spkd, svgc, svd)

	txBytes, err := suite.clientCtx.TxConfig.TxEncoder()(tx)
	require.NoError(t, err)
	suite.ctx = suite.ctx.WithTxBytes(txBytes)

	// Determine gas consumption of antehandler with default params
	before := suite.ctx.GasMeter().GasConsumed()
	ctx, err := antehandler(suite.ctx, tx, false)
	after := ctx.GasMeter().GasConsumed()

	return after - before, err
}

func TestIncrementSequenceDecorator(t *testing.T) {
	suite := SetupTestSuite(t, true)
	suite.txBuilder = suite.clientCtx.TxConfig.NewTxBuilder()

	priv, _, addr := testdata.KeyTestPubAddr()
	acc := suite.accountKeeper.NewAccountWithAddress(suite.ctx, addr)
	require.NoError(t, acc.SetAccountNumber(uint64(50)))
	suite.accountKeeper.SetAccount(suite.ctx, acc)

	msgs := []sdk.Msg{testdata.NewTestMsg(addr)}
	require.NoError(t, suite.txBuilder.SetMsgs(msgs...))
	privs := []cryptotypes.PrivKey{priv}
	accNums := []uint64{suite.accountKeeper.GetAccount(suite.ctx, addr).GetAccountNumber()}
	accSeqs := []uint64{suite.accountKeeper.GetAccount(suite.ctx, addr).GetSequence()}
	feeAmount := testdata.NewTestFeeAmount()
	gasLimit := testdata.NewTestGasLimit()
	suite.txBuilder.SetFeeAmount(feeAmount)
	suite.txBuilder.SetGasLimit(gasLimit)

	tx, err := suite.CreateTestTx(suite.ctx, privs, accNums, accSeqs, suite.ctx.ChainID(), signing.SignMode_SIGN_MODE_DIRECT)
	require.NoError(t, err)

	pubKeyDecorator := ante.NewSetPubKeyDecorator(suite.accountKeeper)
	IncrementSequenceDecorator := ante.NewIncrementSequenceDecorator(suite.accountKeeper)
	antehandler := sdk.ChainAnteDecorators(pubKeyDecorator, IncrementSequenceDecorator)

	testCases := []struct {
		ctx         sdk.Context
		simulate    bool
		expectedSeq uint64
	}{
		{suite.ctx.WithIsReCheckTx(true), false, 1},
		{suite.ctx.WithIsCheckTx(true).WithIsReCheckTx(false), false, 2},
		{suite.ctx.WithIsReCheckTx(true), false, 3},
		{suite.ctx.WithIsReCheckTx(true), false, 4},
		{suite.ctx.WithIsReCheckTx(true), true, 5},
	}

	for i, tc := range testCases {
		t.Run(fmt.Sprintf("%d test", i), func(t *testing.T) {
			_, err = antehandler(tc.ctx, tx, tc.simulate)
			require.NoError(t, err, "unexpected error; tc #%d, %v", i, tc)
			require.Equal(t, tc.expectedSeq, suite.accountKeeper.GetAccount(suite.ctx, addr).GetSequence())
		})
	}
}

func TestAnteHandlerChecks(t *testing.T) {
	suite := SetupTestSuite(t, true)
	suite.txBankKeeper.EXPECT().DenomMetadataV2(gomock.Any(), gomock.Any()).Return(&bankv1beta1.QueryDenomMetadataResponse{}, nil).AnyTimes()

	feeAmount := testdata.NewTestFeeAmount()
	gasLimit := testdata.NewTestGasLimit()
	enabledSignModes := []signing.SignMode{signing.SignMode_SIGN_MODE_DIRECT, signing.SignMode_SIGN_MODE_TEXTUAL, signing.SignMode_SIGN_MODE_LEGACY_AMINO_JSON}
	// Since TEXTUAL is not enabled by default, we create a custom TxConfig
	// here which includes it.
	txConfigOpts := authtx.ConfigOptions{
		TextualCoinMetadataQueryFn: txmodule.NewGRPCCoinMetadataQueryFn(suite.clientCtx),
		EnabledSignModes:           enabledSignModes,
	}

	anteTxConfig, err := authtx.NewTxConfigWithOptions(
		codec.NewProtoCodec(suite.encCfg.InterfaceRegistry),
		txConfigOpts,
	)
	require.NoError(t, err)

	// make block height non-zero to ensure account numbers part of signBytes
	suite.ctx = suite.ctx.WithBlockHeight(1)

	// keys and addresses
	priv1, _, addr1 := testdata.KeyTestPubAddr()

	secp256k1NotOnCurve, _ := secp256k1dcrd.GeneratePrivateKey()
	secp256k1NotOnCurve.Key.SetInt(0) // Setting the key point to 0, results in an invalid point on the curve.
	priv12 := &secp256k1.PrivKey{Key: secp256k1NotOnCurve.Serialize()}
	addr12 := sdk.AccAddress(priv12.PubKey().Address())

	priv2, _, addr2 := testdata.KeyTestPubAddrSecp256R1(t)
	priv3, _, addr3 := testdata.KeyTestPubAddrED25519()

	addrs := []sdk.AccAddress{addr1, addr12, addr2, addr3}

	msgs := make([]sdk.Msg, len(addrs))
	accs := make([]sdk.AccountI, len(addrs))
	// set accounts and create msg for each address
	for i, addr := range addrs {
		acc := suite.accountKeeper.NewAccountWithAddress(suite.ctx, addr)
		require.NoError(t, acc.SetAccountNumber(uint64(i)+1000))
		suite.accountKeeper.SetAccount(suite.ctx, acc)
		msgs[i] = testdata.NewTestMsg(addr)
		accs[i] = acc
	}

	setPubKeyDecorator := ante.NewSetPubKeyDecorator(suite.accountKeeper)
	sigGasConsumeDecorator := ante.NewSigGasConsumeDecorator(suite.accountKeeper, ante.DefaultSigVerificationGasConsumer)
	sigVerificationDecorator := ante.NewSigVerificationDecorator(suite.accountKeeper, anteTxConfig.SignModeHandler())
	IncrementSequenceDecorator := ante.NewIncrementSequenceDecorator(suite.accountKeeper)

	anteHandler := sdk.ChainAnteDecorators(setPubKeyDecorator, sigGasConsumeDecorator, sigVerificationDecorator, IncrementSequenceDecorator)

	type testCase struct {
		name      string
		privs     []cryptotypes.PrivKey
		msg       sdk.Msg
		accNums   []uint64
		accSeqs   []uint64
		shouldErr bool
		suported  bool
	}

	// Secp256r1 keys that are not on curve will fail before even doing any operation i.e when trying to get the pubkey
	testCases := []testCase{
		{"secp256k1_onCurve", []cryptotypes.PrivKey{priv1}, msgs[0], []uint64{accs[0].GetAccountNumber()}, []uint64{0}, false, true},
		{"secp256k1_NotOnCurve", []cryptotypes.PrivKey{priv12}, msgs[1], []uint64{accs[1].GetAccountNumber()}, []uint64{1}, true, true},
		{"secp256r1_onCurve", []cryptotypes.PrivKey{priv2}, msgs[2], []uint64{accs[2].GetAccountNumber()}, []uint64{0}, false, true},
		{"ed255619", []cryptotypes.PrivKey{priv3}, msgs[3], []uint64{accs[2].GetAccountNumber()}, []uint64{3}, true, false},
	}

	for i, tc := range testCases {
		t.Run(fmt.Sprintf("%s key", tc.name), func(t *testing.T) {
			suite.txBuilder = suite.clientCtx.TxConfig.NewTxBuilder() // Create new txBuilder for each test

			require.NoError(t, suite.txBuilder.SetMsgs(tc.msg))

			suite.txBuilder.SetFeeAmount(feeAmount)
			suite.txBuilder.SetGasLimit(gasLimit)

			tx, err := suite.CreateTestTx(suite.ctx, tc.privs, tc.accNums, tc.accSeqs, suite.ctx.ChainID(), signing.SignMode_SIGN_MODE_DIRECT)
			require.NoError(t, err)

			txBytes, err := suite.clientCtx.TxConfig.TxEncoder()(tx)
			require.NoError(t, err)

			byteCtx := suite.ctx.WithTxBytes(txBytes)
			_, err = anteHandler(byteCtx, tx, true)
			if tc.shouldErr {
				require.NotNil(t, err, "TestCase %d: %s did not error as expected", i, tc.name)
				if tc.suported {
					require.ErrorContains(t, err, "not on curve")
				} else {
					require.ErrorContains(t, err, "unsupported key type")
				}
			} else {
				require.Nil(t, err, "TestCase %d: %s errored unexpectedly. Err: %v", i, tc.name, err)
			}
		})
	}
}