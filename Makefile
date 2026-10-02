PYTHON ?= python3
.PHONY: build test verify package inventory lifecycle-test device-check patch-dry-run
build:
	$(PYTHON) scripts/project.py build
test:
	$(PYTHON) scripts/project.py test
verify:
	$(PYTHON) scripts/project.py verify
package:
	$(PYTHON) scripts/project.py package
inventory:
	$(PYTHON) scripts/project.py inventory
lifecycle-test:
	$(PYTHON) scripts/lifecycle.py
device-check:
	$(PYTHON) deploy/device.py check --serial "$(DEVICE_SERIAL)"
patch-dry-run:
	$(PYTHON) patch/scripts/patch.py --app "$(ORIGINAL_APP)" --dry-run
