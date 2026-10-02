# Native multimodal scanning

The original application owns CaptureFrame, image stitching and any existing OCR. The bridge parses the original stitched JPG bytes without resizing, re-encoding, re-OCRing or recompressing them. It checks JPEG format/dimensions and records byte count/MIME/SHA256, then creates a JPEG base64 data URL in the user message with the existing pipeline.

If the request has user text it is preserved. Otherwise a minimal user request asks to solve the image question; no system role is added. The configured model must actually support images, thinking, streaming and tools. The baseline uses `detail=original`; unsupported vision and oversized/invalid inputs return distinct errors. No OCR-first fallback is silently substituted.

Scan requests share reasoning, search, cancellation, source and answer handling with text input. Context retains the latest committed original image, unchanged, for follow-up questions; a newer scan replaces the active image. Tests generate synthetic JPEGs in memory; no user scan or exam image is distributed.
