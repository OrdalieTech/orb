import { createHash } from "node:crypto";
import { mkdir, writeFile } from "node:fs/promises";
import path from "node:path";
import { pathToFileURL } from "node:url";

function tinyBmp(): Uint8Array {
	const buffer = Buffer.alloc(58);
	buffer.write("BM", 0, "ascii");
	buffer.writeUInt32LE(buffer.length, 2);
	buffer.writeUInt32LE(54, 10);
	buffer.writeUInt32LE(40, 14);
	buffer.writeInt32LE(1, 18);
	buffer.writeInt32LE(1, 22);
	buffer.writeUInt16LE(1, 26);
	buffer.writeUInt16LE(24, 28);
	buffer.writeUInt32LE(4, 34);
	buffer[56] = 0xff;
	return buffer;
}

function withExifOrientation(jpeg: Uint8Array, orientation: number): Uint8Array {
	const tiff = Buffer.alloc(26);
	tiff.write("II", 0, "ascii");
	tiff.writeUInt16LE(42, 2);
	tiff.writeUInt32LE(8, 4);
	tiff.writeUInt16LE(1, 8);
	tiff.writeUInt16LE(0x0112, 10);
	tiff.writeUInt16LE(3, 12);
	tiff.writeUInt32LE(1, 14);
	tiff.writeUInt16LE(orientation, 18);
	const payload = Buffer.concat([Buffer.from("Exif\0\0", "binary"), tiff]);
	const header = Buffer.alloc(4);
	header[0] = 0xff;
	header[1] = 0xe1;
	header.writeUInt16BE(payload.length + 2, 2);
	return Buffer.concat([Buffer.from(jpeg).subarray(0, 2), header, payload, Buffer.from(jpeg).subarray(2)]);
}

function sha256(bytes: Uint8Array): string {
	return createHash("sha256").update(bytes).digest("hex");
}

const signatureColors = [
	{ label: "R", rgb: [255, 0, 0] },
	{ label: "G", rgb: [0, 255, 0] },
	{ label: "B", rgb: [0, 0, 255] },
	{ label: "Y", rgb: [255, 255, 0] },
	{ label: "C", rgb: [0, 255, 255] },
	{ label: "M", rgb: [255, 0, 255] },
] as const;

function sampleSignature(photon: any, encoded: string, columns: number, rows: number): string {
	const image = photon.PhotonImage.new_from_byteslice(Buffer.from(encoded, "base64"));
	try {
		const width = image.get_width();
		const height = image.get_height();
		const pixels = image.get_raw_pixels();
		const result: string[] = [];
		for (let row = 0; row < rows; row++) {
			for (let column = 0; column < columns; column++) {
				const x = Math.min(width - 1, Math.floor(((column + 0.5) * width) / columns));
				const y = Math.min(height - 1, Math.floor(((row + 0.5) * height) / rows));
				const offset = (y * width + x) * 4;
				let closest: (typeof signatureColors)[number] = signatureColors[0];
				let closestDistance = Number.POSITIVE_INFINITY;
				for (const candidate of signatureColors) {
					const distance = candidate.rgb.reduce((sum, value, index) => sum + (value - pixels[offset + index]) ** 2, 0);
					if (distance < closestDistance) {
						closest = candidate;
						closestDistance = distance;
					}
				}
				result.push(closest.label);
			}
		}
		return result.join("");
	} finally {
		image.free();
	}
}

function rawRGBA(photon: any, encoded: string): string {
	const image = photon.PhotonImage.new_from_byteslice(Buffer.from(encoded, "base64"));
	try {
		return Buffer.from(image.get_raw_pixels()).toString("base64");
	} finally {
		image.free();
	}
}

export async function generateWP440(upstreamRoot: string, outputRoot: string, upstreamCommit: string): Promise<void> {
	const resizeSource = "packages/coding-agent/src/utils/image-resize-core.ts";
	const resizeFacadeSource = "packages/coding-agent/src/utils/image-resize.ts";
	const processSource = "packages/coding-agent/src/utils/image-process.ts";
	const mimeSource = "packages/coding-agent/src/utils/mime.ts";
	const imageConvertSource = "packages/coding-agent/src/utils/image-convert.ts";
	const exifSource = "packages/coding-agent/src/utils/exif-orientation.ts";
	const photonSource = "packages/coding-agent/src/utils/photon.ts";
	const photonRuntimeSource = "node_modules/@silvia-odwyer/photon-node/photon_rs.js";
	const resizeModule = await import(pathToFileURL(path.join(upstreamRoot, resizeSource)).href);
	const resizeFacade = await import(pathToFileURL(path.join(upstreamRoot, resizeFacadeSource)).href);
	const processModule = await import(pathToFileURL(path.join(upstreamRoot, processSource)).href);
	const mimeModule = await import(pathToFileURL(path.join(upstreamRoot, mimeSource)).href);
	const photon = await import(pathToFileURL(path.join(upstreamRoot, photonRuntimeSource)).href);
	const raw = new Uint8Array([
		255, 0, 0, 255, 0, 255, 0, 255, 0, 0, 255, 255,
		255, 255, 0, 255, 0, 255, 255, 255, 255, 0, 255, 255,
	]);
	const image = new photon.PhotonImage(raw, 3, 2);
	const png = new Uint8Array(image.get_bytes());
	const jpeg = new Uint8Array(image.get_bytes_jpeg(95));
	const webp = new Uint8Array(image.get_bytes_webp());
	image.free();
	const orientationRaw = new Uint8Array(30 * 20 * 4);
	for (let y = 0; y < 20; y++) {
		for (let x = 0; x < 30; x++) {
			const color = signatureColors[Math.floor(y / 10) * 3 + Math.floor(x / 10)].rgb;
			const offset = (y * 30 + x) * 4;
			orientationRaw[offset] = color[0];
			orientationRaw[offset + 1] = color[1];
			orientationRaw[offset + 2] = color[2];
			orientationRaw[offset + 3] = 255;
		}
	}
	const orientationImage = new photon.PhotonImage(orientationRaw, 30, 20);
	const orientationJpeg = new Uint8Array(orientationImage.get_bytes_jpeg(100));
	orientationImage.free();
	const gif = new Uint8Array(Buffer.from("R0lGODlhAQABAIAAAAAAAP///ywAAAAAAQABAAACAUwAOw==", "base64"));
	const formats = [
		{ name: "png-resize", bytes: png, mimeType: "image/png", options: { maxWidth: 2, maxHeight: 2, maxBytes: 1024 * 1024 } },
		{ name: "jpeg-resize", bytes: jpeg, mimeType: "image/jpeg", options: { maxWidth: 2, maxHeight: 2, maxBytes: 1024 * 1024 } },
		{ name: "webp-resize", bytes: webp, mimeType: "image/webp", options: { maxWidth: 2, maxHeight: 2, maxBytes: 1024 * 1024 } },
		{ name: "gif-pass-through", bytes: gif, mimeType: "image/gif", options: { maxWidth: 2, maxHeight: 2, maxBytes: 1024 * 1024 } },
	];
	const formatCases = [];
	for (const fixtureCase of formats) {
		const result = await resizeModule.resizeImageInProcess(fixtureCase.bytes, fixtureCase.mimeType, fixtureCase.options);
		if (!result) throw new Error(`upstream failed to process ${fixtureCase.name}`);
		formatCases.push({
			name: fixtureCase.name,
			inputBase64: Buffer.from(fixtureCase.bytes).toString("base64"),
			inputSHA256: sha256(fixtureCase.bytes),
			mimeType: fixtureCase.mimeType,
			detectedMimeType: mimeModule.detectSupportedImageMimeType(fixtureCase.bytes),
			options: fixtureCase.options,
			expected: {
				originalWidth: result.originalWidth,
				originalHeight: result.originalHeight,
				width: result.width,
				height: result.height,
				wasResized: result.wasResized,
				mimeType: result.mimeType,
				dataStable: !result.wasResized && result.data === Buffer.from(fixtureCase.bytes).toString("base64"),
			},
		});
	}
	const patternedRaw = new Uint8Array(7 * 5 * 4);
	for (let y = 0; y < 5; y++) {
		for (let x = 0; x < 7; x++) {
			const offset = (y * 7 + x) * 4;
			patternedRaw[offset] = (x * 37 + y * 11) % 256;
			patternedRaw[offset + 1] = (x * 19 + y * 53) % 256;
			patternedRaw[offset + 2] = (x * 71 + y * 29) % 256;
			patternedRaw[offset + 3] = 255;
		}
	}
	const patternedImage = new photon.PhotonImage(patternedRaw, 7, 5);
	const patternedPNG = new Uint8Array(patternedImage.get_bytes());
	patternedImage.free();
	const pressureRaw = new Uint8Array(64 * 64 * 4);
	for (let y = 0; y < 64; y++) {
		for (let x = 0; x < 64; x++) {
			const offset = (y * 64 + x) * 4;
			pressureRaw[offset] = (x * 37 + y * 11 + x * y * 3) % 256;
			pressureRaw[offset + 1] = (x * 19 + y * 53 + x * y * 7) % 256;
			pressureRaw[offset + 2] = (x * 71 + y * 29 + x * y * 13) % 256;
			pressureRaw[offset + 3] = 255;
		}
	}
	const pressureImage = new photon.PhotonImage(pressureRaw, 64, 64);
	const pressurePNG = new Uint8Array(pressureImage.get_bytes());
	pressureImage.free();
	const thinRaw = new Uint8Array(9 * 4);
	for (let x = 0; x < 9; x++) {
		thinRaw[x * 4] = x * 28;
		thinRaw[x * 4 + 3] = 255;
	}
	const thinImage = new photon.PhotonImage(thinRaw, 9, 1);
	const thinPNG = new Uint8Array(thinImage.get_bytes());
	thinImage.free();
	const resampleCases = [];
	for (const fixtureCase of [
		{ name: "lanczos3-pixels", bytes: patternedPNG, options: { maxWidth: 4, maxHeight: 3, maxBytes: 1024 * 1024 }, includeRGBA: true },
		{ name: "size-pressure", bytes: pressurePNG, options: { maxWidth: 64, maxHeight: 64, maxBytes: 768 }, includeRGBA: false },
		{ name: "rounded-zero-height", bytes: thinPNG, options: { maxWidth: 4, maxHeight: 1, maxBytes: 1024 * 1024 }, includeRGBA: false },
	]) {
		const result = await resizeModule.resizeImageInProcess(fixtureCase.bytes, "image/png", fixtureCase.options);
		resampleCases.push({
			name: fixtureCase.name,
			inputBase64: Buffer.from(fixtureCase.bytes).toString("base64"),
			inputSHA256: sha256(fixtureCase.bytes),
			mimeType: "image/png",
			options: fixtureCase.options,
			expected: result ? {
				originalWidth: result.originalWidth,
				originalHeight: result.originalHeight,
				width: result.width,
				height: result.height,
				wasResized: result.wasResized,
				mimeType: result.mimeType,
				rawRGBA: fixtureCase.includeRGBA ? rawRGBA(photon, result.data) : undefined,
			} : null,
		});
	}
	const orientationCases = [];
	for (let orientation = 1; orientation <= 8; orientation++) {
		const bytes = withExifOrientation(orientationJpeg, orientation);
		const options = { maxWidth: 15, maxHeight: 15, maxBytes: 1024 * 1024 };
		const result = await resizeModule.resizeImageInProcess(bytes, "image/jpeg", options);
		if (!result) throw new Error(`upstream failed EXIF orientation ${orientation}`);
		const sampleColumns = orientation <= 4 ? 3 : 2;
		const sampleRows = orientation <= 4 ? 2 : 3;
		orientationCases.push({
			orientation,
			inputBase64: Buffer.from(bytes).toString("base64"),
			inputSHA256: sha256(bytes),
			options,
			expected: {
				originalWidth: result.originalWidth,
				originalHeight: result.originalHeight,
				width: result.width,
				height: result.height,
				wasResized: result.wasResized,
				mimeType: result.mimeType,
				note: resizeFacade.formatDimensionNote(result),
				sampleColumns,
				sampleRows,
				pixelSignature: sampleSignature(photon, result.data, sampleColumns, sampleRows),
			},
		});
	}
	const bmp = tinyBmp();
	const pipelineCases = [];
	for (const autoResizeImages of [false, true]) {
		const result = await processModule.processImage(bmp, "image/bmp", { autoResizeImages });
		if (!result.ok) throw new Error(`upstream failed BMP pipeline autoResize=${autoResizeImages}`);
		pipelineCases.push({
			name: autoResizeImages ? "bmp-auto-resize" : "bmp-no-auto-resize",
			inputBase64: Buffer.from(bmp).toString("base64"),
			inputSHA256: sha256(bmp),
			mimeType: "image/bmp",
			autoResizeImages,
			expected: { mimeType: result.mimeType, hints: result.hints, pngMagic: Buffer.from(result.data, "base64").subarray(0, 4).toString("hex") },
		});
	}
	const familyDir = path.join(outputRoot, "WP440");
	await mkdir(familyDir, { recursive: true });
	await writeFile(path.join(familyDir, "manifest.json"), `${JSON.stringify({
		family: "WP440", upstreamCommit, generator: "conformance/extract/wp440-images.ts",
		source: [resizeSource, resizeFacadeSource, processSource, mimeSource, imageConvertSource, exifSource, photonSource, photonRuntimeSource].join(", "),
		files: ["images.json"],
	}, null, 2)}\n`);
	await writeFile(path.join(familyDir, "images.json"), `${JSON.stringify({ schemaVersion: 3, formatCases, resampleCases, orientationCases, pipelineCases }, null, 2)}\n`);
}
