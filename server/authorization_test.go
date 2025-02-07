// Copyright 2020 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package protostruct supports operations on the protocol buffer Struct message.
package server

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/apigee/apigee-remote-service-golib/v2/analytics"
	"github.com/apigee/apigee-remote-service-golib/v2/auth"
	libAuth "github.com/apigee/apigee-remote-service-golib/v2/auth"
	apigeeContext "github.com/apigee/apigee-remote-service-golib/v2/context"
	"github.com/apigee/apigee-remote-service-golib/v2/product"
	"github.com/apigee/apigee-remote-service-golib/v2/quota"
	core "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	v3 "github.com/envoyproxy/go-control-plane/envoy/service/auth/v3"
	"github.com/gogo/googleapis/google/rpc"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestRegister(t *testing.T) {
	opts := []grpc.ServerOption{}
	grpcServer := grpc.NewServer(opts...)
	server := AuthorizationServer{}
	h := &Handler{}
	server.Register(grpcServer, h)
	if h != server.handler {
		t.Errorf("want: %v, got: %v", h, server.handler)
	}
	grpcServer.Stop()
}

func TestCheck(t *testing.T) {

	jwtClaims := &structpb.Struct{
		Fields: map[string]*structpb.Value{
			"apigee": {
				Kind: &structpb.Value_StructValue{
					StructValue: &structpb.Struct{
						Fields: map[string]*structpb.Value{
							"api_product_list": {
								Kind: &structpb.Value_StringValue{
									StringValue: "product1,product2",
								},
							},
						},
					},
				},
			},
		},
	}

	headers := map[string]string{}

	products := product.ProductsNameMap{
		"product1": &product.APIProduct{
			DisplayName: "product1",
		},
	}

	uri := "path?x-api-key=foo"
	req := &v3.CheckRequest{
		Attributes: &v3.AttributeContext{
			Request: &v3.AttributeContext_Request{
				Http: &v3.AttributeContext_HttpRequest{
					Path:    uri,
					Headers: headers,
				},
			},
			MetadataContext: &core.Metadata{
				FilterMetadata: map[string]*structpb.Struct{
					jwtFilterMetadataKey: jwtClaims,
				},
			},
		},
	}

	testAuthMan := &testAuthMan{}
	testProductMan := &testProductMan{
		api:     "api",
		resolve: true,
	}
	testQuotaMan := &testQuotaMan{}
	testAnalyticsMan := &testAnalyticsMan{}
	server := AuthorizationServer{
		handler: &Handler{
			apiKeyClaim:           headerClientID,
			apiHeader:             headerAPI,
			apiKeyHeader:          "x-api-key",
			authMan:               testAuthMan,
			productMan:            testProductMan,
			quotaMan:              testQuotaMan,
			jwtProviderKey:        "apigee",
			appendMetadataHeaders: true,
			analyticsMan:          testAnalyticsMan,
		},
	}

	// no api header
	var resp *v3.CheckResponse
	var err error
	if resp, err = server.Check(context.Background(), req); err != nil {
		t.Errorf("should not get error. got: %s", err)
	}
	if resp.Status.Code != int32(rpc.UNAUTHENTICATED) {
		t.Errorf("got: %d, want: %d", resp.Status.Code, int32(rpc.UNAUTHENTICATED))
	}
	headers[headerAPI] = "api"

	// ErrNoAuth
	testAuthMan.sendAuth(nil, libAuth.ErrNoAuth)
	if resp, err = server.Check(context.Background(), req); err != nil {
		t.Errorf("should not get error. got: %s", err)
	}
	if resp.Status.Code != int32(rpc.UNAUTHENTICATED) {
		t.Errorf("got: %d, want: %d", resp.Status.Code, int32(rpc.UNAUTHENTICATED))
	}

	// ErrBadAuth
	testAuthMan.sendAuth(nil, libAuth.ErrBadAuth)
	if resp, err = server.Check(context.Background(), req); err != nil {
		t.Errorf("should not get error. got: %s", err)
	}
	if resp.Status.Code != int32(rpc.PERMISSION_DENIED) {
		t.Errorf("got: %d, want: %d", resp.Status.Code, int32(rpc.PERMISSION_DENIED))
	}

	// ErrInternalError
	testAuthMan.sendAuth(nil, libAuth.ErrInternalError)
	if resp, err = server.Check(context.Background(), req); err != nil {
		t.Errorf("should not get error. got: %s", err)
	}
	if resp.Status.Code != int32(rpc.INTERNAL) {
		t.Errorf("got: %d, want: %d", resp.Status.Code, int32(rpc.INTERNAL))
	}

	// reset auth error
	testAuthMan.sendAuth(nil, nil)

	// no products
	if resp, err = server.Check(context.Background(), req); err != nil {
		t.Errorf("should not get error. got: %s", err)
	}
	if resp.Status.Code != int32(rpc.PERMISSION_DENIED) {
		t.Errorf("got: %d, want: %d", resp.Status.Code, int32(rpc.PERMISSION_DENIED))
	}

	// no matched products
	testAuthMan.sendAuth(&auth.Context{
		APIProducts: []string{"no match"},
	}, nil)
	testProductMan.products = products
	testProductMan.resolve = false
	if resp, err = server.Check(context.Background(), req); err != nil {
		t.Errorf("should not get error. got: %s", err)
	}
	if resp.Status.Code != int32(rpc.PERMISSION_DENIED) {
		t.Errorf("got: %d, want: %d", resp.Status.Code, int32(rpc.PERMISSION_DENIED))
	}
	testProductMan.resolve = true

	// no products authenticated
	testAuthMan.sendAuth(&auth.Context{
		APIProducts: []string{},
	}, nil)
	if resp, err = server.Check(context.Background(), req); err != nil {
		t.Errorf("should not get error. got: %s", err)
	}
	if resp.Status.Code != int32(rpc.PERMISSION_DENIED) {
		t.Errorf("got: %d, want: %d", resp.Status.Code, int32(rpc.PERMISSION_DENIED))
	}

	// valid auth
	testAuthMan.sendAuth(&auth.Context{
		APIProducts: []string{"product1"},
	}, nil)

	// quota exceeded
	products["product1"].QuotaLimitInt = 2
	testQuotaMan.exceeded = 2
	if resp, err = server.Check(context.Background(), req); err != nil {
		t.Errorf("should not get error. got: %s", err)
	}
	if resp.Status.Code != int32(rpc.RESOURCE_EXHAUSTED) {
		t.Errorf("got: %d, want: %d", resp.Status.Code, int32(rpc.RESOURCE_EXHAUSTED))
	}
	code := resp.HttpResponse.(*v3.CheckResponse_DeniedResponse).DeniedResponse.Status.Code
	if code != http.StatusTooManyRequests {
		t.Errorf("got: %d, want: %d", code, http.StatusTooManyRequests)
	}
	testQuotaMan.exceeded = 0

	// quota error
	testQuotaMan.sendError = errors.New("quota error")
	if resp, err = server.Check(context.Background(), req); err != nil {
		t.Errorf("should not get error. got: %s", err)
	}
	if resp.Status.Code != int32(rpc.INTERNAL) {
		t.Errorf("got: %d, want: %d", resp.Status.Code, int32(rpc.INTERNAL))
	}
	testQuotaMan.sendError = nil

	// all OK
	if resp, err = server.Check(context.Background(), req); err != nil {
		t.Errorf("should not get error. got: %s", err)
	}
	if resp.Status.Code != int32(rpc.OK) {
		t.Errorf("got: %d, want: %d", resp.Status.Code, int32(rpc.OK))
	}

	// bad api in context metadata
	req.Attributes.ContextExtensions = map[string]string{}
	req.Attributes.ContextExtensions[apiContextKey] = "bad-api"
	if resp, err = server.Check(context.Background(), req); err != nil {
		t.Errorf("should not get error. got: %s", err)
	}
	if resp.Status.Code != int32(rpc.PERMISSION_DENIED) {
		t.Errorf("got: %d, want: %d", resp.Status.Code, int32(rpc.PERMISSION_DENIED))
	}

	// good api in context supersedes even if api header is bad
	headers[headerAPI] = "bad-api"
	req.Attributes.ContextExtensions[apiContextKey] = "api"
	if resp, err = server.Check(context.Background(), req); err != nil {
		t.Errorf("should not get error. got: %s", err)
	}
	if resp.Status.Code != int32(rpc.OK) {
		t.Errorf("got: %d, want: %d", resp.Status.Code, int32(rpc.OK))
	}
	delete(req.Attributes.ContextExtensions, apiContextKey)
	headers[headerAPI] = "api"

	// testAuthMan.ctx
	if testAuthMan.apiKey != "foo" {
		t.Errorf("got: %s, want: %s", testAuthMan.apiKey, "foo")
	}
	// testAuthMan.claims
	if testAuthMan.apiKeyClaimKey != headerClientID {
		t.Errorf("got: %s, want: %s", testAuthMan.apiKeyClaimKey, headerClientID)
	}

	// non-existing jwtProviderKey
	server.handler.jwtProviderKey = "not-apigee"
	if resp, err = server.Check(context.Background(), req); err != nil {
		t.Errorf("should not get error. got: %s", err)
	}
	if resp.Status.Code != int32(rpc.OK) {
		t.Errorf("got: %d, want: %d", resp.Status.Code, int32(rpc.OK))
	}

	// testAuthMan.claims should be nil
	if len(testAuthMan.claims) != 0 {
		t.Errorf("got: %d, want: empty claims", len(testAuthMan.claims))
	}

	// empty jwtProviderKey to enter the claims loop
	server.handler.jwtProviderKey = ""
	if resp, err = server.Check(context.Background(), req); err != nil {
		t.Errorf("should not get error. got: %s", err)
	}
	if resp.Status.Code != int32(rpc.OK) {
		t.Errorf("got: %d, want: %d", resp.Status.Code, int32(rpc.OK))
	}

	// testAuthMan.claims should be nil
	if len(testAuthMan.claims) != 1 {
		t.Errorf("got: %d, want: claims length to be 1", len(testAuthMan.claims))
	}

	// check deny when allowUnauthorized = true
	server.handler.allowUnauthorized = true
	testProductMan.resolve = false
	if resp, err = server.Check(context.Background(), req); err != nil {
		t.Errorf("should not get error. got: %s", err)
	}
	if resp.Status.Code != int32(rpc.OK) {
		t.Errorf("got: %d, want: %d", resp.Status.Code, int32(rpc.OK))
	}

	// improper context, not multitenant
	server.handler.envName = "test"
	req.Attributes.ContextExtensions[envContextKey] = "prod"
	if resp, err = server.Check(context.Background(), req); err != nil {
		t.Errorf("should not get error. got: %s", err)
	}
	if resp.Status.Code != int32(rpc.INTERNAL) {
		t.Errorf("got: %d, want: %d", resp.Status.Code, int32(rpc.INTERNAL))
	}

	// multitenant missing context
	server.handler.envName = "*"
	server.handler.isMultitenant = true
	delete(req.Attributes.ContextExtensions, envContextKey)
	if resp, err = server.Check(context.Background(), req); err != nil {
		t.Errorf("should not get error. got: %s", err)
	}
	if resp.Status.Code != int32(rpc.INTERNAL) {
		t.Errorf("got: %d, want: %d", resp.Status.Code, int32(rpc.INTERNAL))
	}

	// multitenant receives context
	req.Attributes.ContextExtensions[envContextKey] = "test"
	if resp, err = server.Check(context.Background(), req); err != nil {
		t.Errorf("should not get error. got: %s", err)
	}
	if resp.Status.Code != int32(rpc.OK) {
		t.Errorf("got: %d, want: %d", resp.Status.Code, int32(rpc.OK))
	}
}

func TestImmediateAnalytics(t *testing.T) {

	jwtClaims := &structpb.Struct{}

	headers := map[string]string{
		"User-Agent":      "User-Agent",
		"X-Forwarded-For": "X-Forwarded-For",
		headerAPI:         "api",
	}

	requestPath := "path"
	uri := requestPath + "?x-api-key=foo"
	requestTime := time.Now()
	nowProto := timestamppb.New(requestTime)

	req := &v3.CheckRequest{
		Attributes: &v3.AttributeContext{
			Request: &v3.AttributeContext_Request{
				Http: &v3.AttributeContext_HttpRequest{
					Path:    uri,
					Headers: headers,
					Method:  http.MethodGet,
				},
				Time: nowProto,
			},
			MetadataContext: &core.Metadata{
				FilterMetadata: map[string]*structpb.Struct{
					jwtFilterMetadataKey: jwtClaims,
				},
			},
		},
	}

	testAuthMan := &testAuthMan{}
	ac := &auth.Context{
		ClientID:       "client id",
		AccessToken:    "token",
		Application:    "app",
		APIProducts:    []string{"product1"},
		Expires:        time.Now(),
		DeveloperEmail: "email",
		Scopes:         []string{"scope"},
		APIKey:         "apikey",
	}
	testAuthMan.sendAuth(ac, auth.ErrBadAuth)

	testProductMan := &testProductMan{
		resolve: true,
		api:     "api",
	}
	testQuotaMan := &testQuotaMan{}
	testAnalyticsMan := &testAnalyticsMan{}
	server := AuthorizationServer{
		handler: &Handler{
			orgName:               "org",
			envName:               "env",
			apiKeyClaim:           headerClientID,
			apiHeader:             headerAPI,
			apiKeyHeader:          "x-api-key",
			authMan:               testAuthMan,
			productMan:            testProductMan,
			quotaMan:              testQuotaMan,
			analyticsMan:          testAnalyticsMan,
			jwtProviderKey:        "apigee",
			appendMetadataHeaders: true,
		},
	}

	var resp *v3.CheckResponse
	resp, err := server.Check(context.Background(), req)
	if err != nil {
		t.Errorf("should not get error. got: %s", err)
	}
	if resp.Status.Code != int32(rpc.PERMISSION_DENIED) {
		t.Errorf("got: %d, want: %d", resp.Status.Code, int32(rpc.PERMISSION_DENIED))
	}

	if len(testAnalyticsMan.records) != 1 {
		t.Fatalf("got: %d, want: %d", len(testAnalyticsMan.records), 1)
	}

	got := testAnalyticsMan.records[0]
	want := analytics.Record{
		ClientReceivedStartTimestamp: requestTime.UnixNano() / 1000000,
		ClientReceivedEndTimestamp:   requestTime.UnixNano() / 1000000,
		TargetSentStartTimestamp:     0,
		TargetSentEndTimestamp:       0,
		TargetReceivedStartTimestamp: 0,
		TargetReceivedEndTimestamp:   0,
		RecordType:                   "APIAnalytics",
		APIProxy:                     headers[headerAPI],
		RequestURI:                   uri,
		RequestPath:                  requestPath,
		RequestVerb:                  http.MethodGet,
		ClientIP:                     headers["X-Forwarded-For"],
		UserAgent:                    headers["User-Agent"],
		APIProxyRevision:             0,
		ResponseStatusCode:           http.StatusForbidden,
		DeveloperEmail:               ac.DeveloperEmail,
		DeveloperApp:                 ac.Application,
		AccessToken:                  ac.AccessToken,
		ClientID:                     ac.ClientID,
		APIProduct:                   ac.APIProducts[0],
		Organization:                 server.handler.orgName,
		Environment:                  server.handler.envName,
		GatewaySource:                gatewaySource,
		// the following fields vary, ignore them
		ClientSentStartTimestamp: got.ClientSentStartTimestamp,
		ClientSentEndTimestamp:   got.ClientSentEndTimestamp,
		GatewayFlowID:            got.GatewayFlowID,
	}

	if got.ClientSentStartTimestamp < requestTime.Unix() {
		t.Errorf("got: %d, want >=: %d", got.ClientSentStartTimestamp, requestTime.Unix())
	}
	if got.ClientSentEndTimestamp < got.ClientSentStartTimestamp {
		t.Errorf("got: %d, want >=: %d", got.ClientSentEndTimestamp, got.ClientSentStartTimestamp)
	}
	if got.GatewayFlowID == "" {
		t.Errorf("GatewayFlowID should not be empty")
	}

	if !reflect.DeepEqual(got, want) {
		t.Errorf("got: %#v, want: %#v", got, want)
	}
}

type testAuthMan struct {
	ctx             apigeeContext.Context
	apiKey          string
	claims          map[string]interface{}
	apiKeyClaimKey  string
	makeContextFunc func(ctx apigeeContext.Context) (*auth.Context, error)
}

func (a *testAuthMan) Close() {}
func (a *testAuthMan) Authenticate(ctx apigeeContext.Context, apiKey string, claims map[string]interface{},
	apiKeyClaimKey string) (*auth.Context, error) {
	a.ctx = ctx
	a.apiKey = apiKey
	a.claims = claims
	a.apiKeyClaimKey = apiKeyClaimKey

	return a.makeContextFunc(ctx)
}

func (a *testAuthMan) sendAuth(ac *auth.Context, err error) {
	if ac == nil {
		ac = &auth.Context{}
	}
	a.makeContextFunc = func(ctx apigeeContext.Context) (*auth.Context, error) {
		ac.Context = ctx
		return ac, err
	}
}

type testProductMan struct {
	products map[string]*product.APIProduct
	api      string
	resolve  bool
}

func (p *testProductMan) Close() {}
func (p *testProductMan) Products() product.ProductsNameMap {
	return p.products
}
func (p *testProductMan) Authorize(ac *auth.Context, api, path, method string) []product.AuthorizedOperation {
	if !p.resolve {
		return nil
	}
	if api != p.api {
		return nil
	}
	values := []product.AuthorizedOperation{}
	for _, p := range p.products {
		values = append(values, product.AuthorizedOperation{
			ID:         p.DisplayName,
			QuotaLimit: 42,
		})
	}
	return values
}

type testQuotaMan struct {
	exceeded  int64
	sendError error
}

func (q *testQuotaMan) Start() {}
func (q *testQuotaMan) Close() {}
func (q *testQuotaMan) Apply(auth *auth.Context, p product.AuthorizedOperation, args quota.Args) (*quota.Result, error) {
	if q.sendError != nil {
		return nil, q.sendError
	}
	return &quota.Result{
		Exceeded: q.exceeded,
	}, nil
}
