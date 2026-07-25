package components_test

import (
	"strings"
	"testing"

	"github.com/ericfisherdev/nestorage/web/components"
)

func TestReturnRequestSection_CanRequest_RendersRequestForm(t *testing.T) {
	view := components.ReturnRequestSectionView{ItemID: "item-1", CSRFToken: "tok", CanRequest: true}
	out := renderString(t, components.ReturnRequestSection(view))

	if !strings.Contains(out, "Request return") {
		t.Errorf("ReturnRequestSection(CanRequest) missing the request-return control: %s", out)
	}
	if strings.Contains(out, "Cancel request") {
		t.Errorf("ReturnRequestSection(CanRequest) should not also render the cancel control: %s", out)
	}
	if !strings.Contains(out, `action="/items/item-1/return-requests"`) {
		t.Errorf("ReturnRequestSection(CanRequest) form action missing/wrong: %s", out)
	}
	if !strings.Contains(out, `value="tok"`) {
		t.Errorf("ReturnRequestSection(CanRequest) missing the CSRF token: %s", out)
	}
}

func TestReturnRequestSection_OpenRequest_RendersCancelForm(t *testing.T) {
	view := components.ReturnRequestSectionView{ItemID: "item-1", CSRFToken: "tok", OpenRequestID: "req-1"}
	out := renderString(t, components.ReturnRequestSection(view))

	if !strings.Contains(out, "Cancel request") {
		t.Errorf("ReturnRequestSection(open request) missing the cancel control: %s", out)
	}
	if strings.Contains(out, "Request return") {
		t.Errorf("ReturnRequestSection(open request) should not also render the request form: %s", out)
	}
	if !strings.Contains(out, `action="/items/item-1/return-requests/req-1/cancel"`) {
		t.Errorf("ReturnRequestSection(open request) form action missing/wrong: %s", out)
	}
}

func TestReturnRequestSection_Neither_RendersNothing(t *testing.T) {
	view := components.ReturnRequestSectionView{ItemID: "item-1", CSRFToken: "tok"}
	out := renderString(t, components.ReturnRequestSection(view))

	if strings.Contains(out, "Request return") || strings.Contains(out, "Cancel request") {
		t.Errorf("ReturnRequestSection(neither) should render no control: %s", out)
	}
}
