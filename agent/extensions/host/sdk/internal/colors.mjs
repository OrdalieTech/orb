// orb-extension-sdk: pi-tui color utilities, transpiled from upstream
// packages/tui/src/oklab.ts and colors.ts (pi 1.0.0, commit a13d35a7). OKLab and
// OKHSL follow Björn Ottosson (https://bottosson.github.io/posts/colorpicker/,
// MIT, © 2021 Björn Ottosson); the rest is MIT © Mario Zechner.
/**
 * Oklab and OKHSL <-> sRGB conversion. `colors.ts` builds its OKLCH, OKHSL, and color mixing on it.
 *
 * Oklab and OKHSL are Björn Ottosson's color spaces; OKHSL's saturation is relative to the sRGB gamut at
 * each hue and lightness. This is a port of his reference implementation (https://bottosson.github.io/posts/colorpicker/),
 * Copyright (c) 2021 Björn Ottosson, used under the MIT license:
 *
 * Permission is hereby granted, free of charge, to any person obtaining a copy of this software and
 * associated documentation files (the "Software"), to deal in the Software without restriction, including
 * without limitation the rights to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
 * copies of the Software, and to permit persons to whom the Software is furnished to do so, subject to the
 * following conditions: The above copyright notice and this permission notice shall be included in all
 * copies or substantial portions of the Software. THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY
 * KIND, EXPRESS OR IMPLIED.
 */
const multiply = (m, [x, y, z]) => m.map((row) => row[0] * x + row[1] * y + row[2] * z);
// ============================================================================
// OKHSL <-> sRGB
// ============================================================================
const LINEAR_SRGB_TO_LMS = [
    [0.4122214694707629, 0.5363325372617349, 0.0514459932675022],
    [0.2119034958178251, 0.6806995506452344, 0.1073969535369405],
    [0.0883024591900564, 0.2817188391361215, 0.6299787016738222],
];
const LMS_TO_LAB = [
    [0.210454268309314, 0.793617774702305, -0.0040720430116193],
    [1.9779985324311684, -2.42859224204858, 0.450593709617411],
    [0.0259040424655478, 0.7827717124575296, -0.8086757549230774],
];
const LAB_TO_LMS = [
    [1, 0.3963377773761749, 0.2158037573099136],
    [1, -0.1055613458156586, -0.0638541728258133],
    [1, -0.0894841775298119, -1.2914855480194092],
];
const LMS_TO_LINEAR_SRGB = [
    [4.0767416360759583, -3.3077115392580629, 0.2309699031821043],
    [-1.2684379732850315, 2.6097573492876882, -0.341319376002657],
    [-0.0041960761386756, -0.7034186179359362, 1.7076146940746117],
];
/**
 * Per sRGB channel (red, green, blue): the (a, b) half-plane where that channel clips
 * first, and the polynomial approximating the maximum saturation there.
 */
const SATURATION_FIT = [
    [
        [-1.8817031, -0.80936501],
        [1.19086277, 1.76576728, 0.59662641, 0.75515197, 0.56771245],
    ],
    [
        [1.8144408, -1.19445267],
        [0.73956515, -0.45954404, 0.08285427, 0.12541073, -0.14503204],
    ],
    [
        [0.13110758, 1.81333971],
        [1.35733652, -0.00915799, -1.1513021, -0.50559606, 0.00692167],
    ],
];
const K1 = 0.206;
const K2 = 0.03;
const K3 = (1 + K1) / (1 + K2);
/** Oklab lightness to OKHSL lightness. */
export const oklabToOkhslLightness = (x) => 0.5 * (K3 * x - K1 + Math.sqrt((K3 * x - K1) ** 2 + 4 * K2 * K3 * x));
/** OKHSL lightness to Oklab lightness. */
const okhslToOklabLightness = (x) => (x * x + K1 * x) / (K3 * (x + K2));
/** sRGB transfer function: linear to encoded channel, both 0-1. */
const linearToSrgb = (value) => value > 0.0031308 ? 1.055 * value ** (1 / 2.4) - 0.055 : 12.92 * value;
/** Inverse sRGB transfer function: encoded to linear channel, both 0-1. */
const srgbToLinear = (value) => (value <= 0.04045 ? value / 12.92 : ((value + 0.055) / 1.055) ** 2.4);
/** Oklab [L, a, b] to linear sRGB [r, g, b] (0-1, may leave the gamut). */
export function oklabToLinearSrgb(lab) {
    return multiply(LMS_TO_LINEAR_SRGB, multiply(LAB_TO_LMS, lab).map((value) => value ** 3));
}
/** Linear sRGB [r, g, b] (0-1) to Oklab [L, a, b]. */
function linearSrgbToOklab(rgb) {
    return multiply(LMS_TO_LAB, multiply(LINEAR_SRGB_TO_LMS, rgb).map(Math.cbrt));
}
/** sRGB channels (0-255) to Oklab [L, a, b]. */
export function rgbToOklab({ r, g, b }) {
    return linearSrgbToOklab([r / 255, g / 255, b / 255].map(srgbToLinear));
}
/** Linear sRGB [r, g, b] to sRGB channels (0-255, rounded), clipping out-of-gamut channels. */
export function linearSrgbToRgb(linear) {
    const [r, g, b] = linear.map((value) => Math.round(Math.min(1, Math.max(0, linearToSrgb(value))) * 255));
    return { r, g, b };
}
/** Rate of change of each cube-root LMS component along a chroma direction (a, b). */
function lmsSlopes(a, b) {
    return [LAB_TO_LMS[0], LAB_TO_LMS[1], LAB_TO_LMS[2]].map((row) => row[1] * a + row[2] * b);
}
/** Largest saturation (C/L) inside sRGB for hue (a, b): polynomial fit plus one Halley step. */
function maxSaturation(a, b) {
    const channel = SATURATION_FIT.findIndex(([[x, y]], index) => index === 2 || x * a + y * b > 1);
    const [k0, k1, k2, k3, k4] = SATURATION_FIT[channel][1];
    const weights = LMS_TO_LINEAR_SRGB[channel];
    const saturation = k0 + k1 * a + k2 * b + k3 * a * a + k4 * a * b;
    const slopes = lmsSlopes(a, b);
    const base = slopes.map((k) => 1 + saturation * k);
    const dot = (values) => values.reduce((sum, value, index) => sum + weights[index] * value, 0);
    const f = dot(base.map((value) => value ** 3));
    const f1 = dot(base.map((value, index) => 3 * slopes[index] * value ** 2));
    const f2 = dot(base.map((value, index) => 6 * slopes[index] ** 2 * value));
    return saturation - (f * f1) / (f1 * f1 - 0.5 * f * f2);
}
/** Oklab lightness and chroma of the most saturated sRGB color of hue (a, b). */
function cusp(a, b) {
    const saturation = maxSaturation(a, b);
    const lightness = Math.cbrt(1 / Math.max(...oklabToLinearSrgb([1, saturation * a, saturation * b])));
    return [lightness, lightness * saturation];
}
/** Chroma where the constant-lightness line at `lightness` leaves the sRGB gamut. */
function maxChroma(a, b, lightness, [cuspL, cuspC]) {
    if (lightness <= cuspL)
        return (cuspC * lightness) / cuspL;
    // Upper half: triangle edge, then one Halley step against each channel reaching 1.
    const t = (cuspC * (lightness - 1)) / (cuspL - 1);
    const slopes = lmsSlopes(a, b);
    const lms = slopes.map((k) => lightness + t * k);
    const cubes = lms.map((value) => value ** 3);
    const first = lms.map((value, index) => 3 * slopes[index] * value ** 2);
    const second = lms.map((value, index) => 6 * slopes[index] ** 2 * value);
    const dot = (row, values) => row[0] * values[0] + row[1] * values[1] + row[2] * values[2];
    const steps = LMS_TO_LINEAR_SRGB.map((row) => {
        const f = dot(row, cubes) - 1;
        const f1 = dot(row, first);
        const f2 = dot(row, second);
        const u = f1 / (f1 * f1 - 0.5 * f * f2);
        return u >= 0 ? -f * u : Number.MAX_VALUE;
    });
    return t + Math.min(...steps);
}
/** OKHSL's chroma reference points at lightness L and hue (a, b): [c0, cMid, cMax]. */
function chromaStops(L, a, b) {
    const peak = cusp(a, b);
    const cMax = maxChroma(a, b, L, peak);
    const k = cMax / Math.min(L * (peak[1] / peak[0]), (1 - L) * (peak[1] / (1 - peak[0])));
    const midS = 0.11516993 +
        1 /
            (7.4477897 +
                4.1590124 * b +
                a *
                    (-2.19557347 +
                        1.75198401 * b +
                        a * (-2.13704948 - 10.02301043 * b + a * (-4.24894561 + 5.38770819 * b + 4.69891013 * a))));
    const midT = 0.11239642 +
        1 /
            (1.6132032 -
                0.68124379 * b +
                a *
                    (0.40370612 +
                        0.90148123 * b +
                        a * (-0.27087943 + 0.6122399 * b + a * (0.00299215 - 0.45399568 * b - 0.14661872 * a))));
    const cMid = 0.9 * k * Math.sqrt(Math.sqrt(1 / (1 / (L * midS) ** 4 + 1 / ((1 - L) * midT) ** 4)));
    const c0 = Math.sqrt(1 / (1 / (L * 0.4) ** 2 + 1 / ((1 - L) * 0.8) ** 2));
    return [c0, cMid, cMax];
}
/**
 * Convert OKHSL to sRGB channels (0-255, rounded), clipping out-of-gamut channels.
 * @param hue Hue in degrees.
 * @param saturation Saturation, 0-1.
 * @param lightness Lightness, 0-1.
 */
export function okhslToRgb(hue, saturation, lightness) {
    const L = okhslToOklabLightness(lightness);
    let lab = [L, 0, 0];
    if (L > 0 && L < 1 && saturation > 0) {
        const angle = (2 * Math.PI * (((hue % 360) + 360) % 360)) / 360;
        const a = Math.cos(angle);
        const b = Math.sin(angle);
        const [c0, cMid, cMax] = chromaStops(L, a, b);
        // Chroma rises from 0 through cMid at s = 0.8 to cMax at s = 1.
        let chroma;
        if (saturation < 0.8) {
            const t = 1.25 * saturation;
            const k1 = 0.8 * c0;
            chroma = (t * k1) / (1 - (1 - k1 / cMid) * t);
        }
        else {
            const t = 5 * (saturation - 0.8);
            const k1 = (0.2 * cMid ** 2 * 1.25 ** 2) / c0;
            chroma = cMid + (t * k1) / (1 - (1 - k1 / (cMax - cMid)) * t);
        }
        lab = [L, chroma * a, chroma * b];
    }
    return linearSrgbToRgb(oklabToLinearSrgb(lab));
}
/**
 * Convert sRGB channels (0-255) to OKHSL.
 * @returns Hue `h` in degrees (0 for grays), saturation `s` and lightness `l` 0-1.
 */
export function rgbToOkhsl(rgb) {
    const [L, labA, labB] = rgbToOklab(rgb);
    const chroma = Math.hypot(labA, labB);
    const lightness = oklabToOkhslLightness(L);
    if (chroma < 1e-9 || lightness <= 0 || lightness >= 1)
        return { h: 0, s: 0, l: lightness };
    const hue = ((Math.atan2(labB, labA) * 180) / Math.PI + 360) % 360;
    const [c0, cMid, cMax] = chromaStops(L, labA / chroma, labB / chroma);
    let saturation;
    if (chroma < cMid) {
        const k1 = 0.8 * c0;
        saturation = 0.8 * (chroma / (k1 + (1 - k1 / cMid) * chroma));
    }
    else {
        const k1 = (0.2 * cMid ** 2 * 1.25 ** 2) / c0;
        const offset = chroma - cMid;
        saturation = 0.8 + 0.2 * (offset / (k1 + (1 - k1 / (cMax - cMid)) * offset));
    }
    return { h: hue, s: Math.min(1, Math.max(0, saturation)), l: lightness };
}

function requireFinite(value, name) {
    if (!Number.isFinite(value))
        throw new Error(`${name} must be finite`);
}
export function indexedColor(index) {
    if (!Number.isInteger(index) || index < 0 || index > 255) {
        throw new Error(`ANSI color index must be an integer from 0 to 255: ${index}`);
    }
    return Object.freeze({ kind: "indexed", index });
}
export function rgbColor(r, g, b) {
    for (const [name, value] of [
        ["r", r],
        ["g", g],
        ["b", b],
    ]) {
        requireFinite(value, name);
        if (value < 0 || value > 255)
            throw new Error(`${name} must be between 0 and 255: ${value}`);
    }
    return Object.freeze({ kind: "rgb", r, g, b });
}
export function oklchColor(l, c, h) {
    requireFinite(l, "l");
    requireFinite(c, "c");
    requireFinite(h, "h");
    if (l < 0 || l > 1)
        throw new Error(`l must be between 0 and 1: ${l}`);
    if (c < 0)
        throw new Error(`c must not be negative: ${c}`);
    return Object.freeze({ kind: "oklch", l, c, h: ((h % 360) + 360) % 360 });
}
const NUMBER_PATTERN = String.raw `[+-]?(?:\d+(?:\.\d*)?|\.\d+)(?:e[+-]?\d+)?`;
const OKLCH_PATTERN = new RegExp(`^oklch\\(\\s*(${NUMBER_PATTERN})(%)?\\s+(${NUMBER_PATTERN})\\s+(${NUMBER_PATTERN})(?:deg)?\\s*\\)$`, "i");
const OKHSL_PATTERN = new RegExp(`^okhsl\\(\\s*(${NUMBER_PATTERN})(?:deg)?\\s+(${NUMBER_PATTERN})(%)?\\s+(${NUMBER_PATTERN})(%)?\\s*\\)$`, "i");
/**
 * An OKHSL color, converted to sRGB. Saturation is relative to the sRGB gamut at the hue and lightness,
 * so equal saturation looks equally colorful across hues and lightness.
 * @param h Hue in degrees.
 * @param s Saturation, 0-1.
 * @param l Lightness, 0-1.
 */
export function okhslColor(h, s, l) {
    requireFinite(h, "h");
    requireFinite(s, "s");
    requireFinite(l, "l");
    if (s < 0 || s > 1)
        throw new Error(`s must be between 0 and 1: ${s}`);
    if (l < 0 || l > 1)
        throw new Error(`l must be between 0 and 1: ${l}`);
    const { r, g, b } = okhslToRgb(h, s, l);
    return rgbColor(r, g, b);
}
export function colorToOkhsl(color) {
    return rgbToOkhsl(colorToRgb(color));
}
export function parseColor(value) {
    if (typeof value === "number")
        return indexedColor(value);
    const hex = /^#([\da-f]{3}|[\da-f]{6})$/i.exec(value);
    if (hex) {
        const digits = hex[1].length === 3 ? [...hex[1]].map((digit) => digit + digit).join("") : hex[1];
        return rgbColor(Number.parseInt(digits.slice(0, 2), 16), Number.parseInt(digits.slice(2, 4), 16), Number.parseInt(digits.slice(4, 6), 16));
    }
    const oklch = OKLCH_PATTERN.exec(value);
    if (oklch) {
        const lightness = Number.parseFloat(oklch[1]) / (oklch[2] ? 100 : 1);
        return oklchColor(lightness, Number.parseFloat(oklch[3]), Number.parseFloat(oklch[4]));
    }
    const okhsl = OKHSL_PATTERN.exec(value);
    if (okhsl) {
        const saturation = Number.parseFloat(okhsl[2]) / (okhsl[3] ? 100 : 1);
        const lightness = Number.parseFloat(okhsl[4]) / (okhsl[5] ? 100 : 1);
        return okhslColor(Number.parseFloat(okhsl[1]), saturation, lightness);
    }
    throw new Error(`Invalid color value: ${value}`);
}
const BASIC_COLORS = [
    { r: 0, g: 0, b: 0 },
    { r: 128, g: 0, b: 0 },
    { r: 0, g: 128, b: 0 },
    { r: 128, g: 128, b: 0 },
    { r: 0, g: 0, b: 128 },
    { r: 128, g: 0, b: 128 },
    { r: 0, g: 128, b: 128 },
    { r: 192, g: 192, b: 192 },
    { r: 128, g: 128, b: 128 },
    { r: 255, g: 0, b: 0 },
    { r: 0, g: 255, b: 0 },
    { r: 255, g: 255, b: 0 },
    { r: 0, g: 0, b: 255 },
    { r: 255, g: 0, b: 255 },
    { r: 0, g: 255, b: 255 },
    { r: 255, g: 255, b: 255 },
];
const CUBE_VALUES = [0, 95, 135, 175, 215, 255];
const GRAY_VALUES = Array.from({ length: 24 }, (_, index) => 8 + index * 10);
function indexedToRgb(index) {
    if (index < 16)
        return { ...BASIC_COLORS[index] };
    if (index < 232) {
        const cubeIndex = index - 16;
        return {
            r: CUBE_VALUES[Math.floor(cubeIndex / 36)],
            g: CUBE_VALUES[Math.floor((cubeIndex % 36) / 6)],
            b: CUBE_VALUES[cubeIndex % 6],
        };
    }
    const gray = 8 + (index - 232) * 10;
    return { r: gray, g: gray, b: gray };
}
function isInSrgbGamut(linear) {
    const epsilon = 1e-7;
    return linear.every((channel) => channel >= -epsilon && channel <= 1 + epsilon);
}
function oklchToRgb({ l, c, h }) {
    // Gamut mapping keeps the hue fixed, so its direction is computed once and scaled by chroma.
    const radians = (h * Math.PI) / 180;
    const cos = Math.cos(radians);
    const sin = Math.sin(radians);
    const atChroma = (chroma) => oklabToLinearSrgb([l, chroma * cos, chroma * sin]);
    const direct = atChroma(c);
    if (isInSrgbGamut(direct))
        return linearSrgbToRgb(direct);
    // Reduce chroma until the color fits. The achromatic color is always in gamut, so it is the
    // fallback when no bisection step fits, e.g. `oklch(100% 0.3 150)` must map to white.
    let linear = atChroma(0);
    let low = 0;
    let high = c;
    for (let index = 0; index < 20; index++) {
        const chroma = (low + high) / 2;
        const candidate = atChroma(chroma);
        if (isInSrgbGamut(candidate)) {
            low = chroma;
            linear = candidate;
        }
        else {
            high = chroma;
        }
    }
    return linearSrgbToRgb(linear);
}
export function colorToRgb(color) {
    switch (color.kind) {
        case "indexed":
            return indexedToRgb(color.index);
        case "rgb":
            return { r: color.r, g: color.g, b: color.b };
        case "oklch":
            return oklchToRgb(color);
    }
}
export function colorToOklch(color) {
    if (color.kind === "oklch")
        return { l: color.l, c: color.c, h: color.h };
    const [l, a, b] = rgbToOklab(colorToRgb(color));
    return { l, c: Math.hypot(a, b), h: ((Math.atan2(b, a) * 180) / Math.PI + 360) % 360 };
}
export function colorToHex(color) {
    const { r, g, b } = colorToRgb(color);
    const channel = (value) => Math.round(value).toString(16).padStart(2, "0");
    return `#${channel(r)}${channel(g)}${channel(b)}`;
}
export function mixColors(first, second, amount, space = "oklch") {
    requireFinite(amount, "amount");
    if (amount < 0 || amount > 1)
        throw new Error(`amount must be between 0 and 1: ${amount}`);
    if (space === "srgb") {
        const a = colorToRgb(first);
        const b = colorToRgb(second);
        return rgbColor(a.r + (b.r - a.r) * amount, a.g + (b.g - a.g) * amount, a.b + (b.b - a.b) * amount);
    }
    const a = colorToOklch(first);
    const b = colorToOklch(second);
    const firstHue = a.c < 1e-7 ? b.h : a.h;
    const secondHue = b.c < 1e-7 ? firstHue : b.h;
    const hueDelta = ((secondHue - firstHue + 540) % 360) - 180;
    return oklchColor(a.l + (b.l - a.l) * amount, a.c + (b.c - a.c) * amount, firstHue + hueDelta * amount);
}
function findClosest(values, target) {
    let closestIndex = 0;
    let closestDistance = Infinity;
    for (let index = 0; index < values.length; index++) {
        const distance = Math.abs(target - values[index]);
        if (distance < closestDistance) {
            closestIndex = index;
            closestDistance = distance;
        }
    }
    return closestIndex;
}
function colorDistance(first, second) {
    const dr = first.r - second.r;
    const dg = first.g - second.g;
    const db = first.b - second.b;
    return dr * dr * 0.299 + dg * dg * 0.587 + db * db * 0.114;
}
function rgbToAnsi256(color) {
    const rIndex = findClosest(CUBE_VALUES, color.r);
    const gIndex = findClosest(CUBE_VALUES, color.g);
    const bIndex = findClosest(CUBE_VALUES, color.b);
    const cubeColor = { r: CUBE_VALUES[rIndex], g: CUBE_VALUES[gIndex], b: CUBE_VALUES[bIndex] };
    const cubeIndex = 16 + 36 * rIndex + 6 * gIndex + bIndex;
    const gray = Math.round(0.299 * color.r + 0.587 * color.g + 0.114 * color.b);
    const grayOffset = findClosest(GRAY_VALUES, gray);
    const grayValue = GRAY_VALUES[grayOffset];
    const spread = Math.max(color.r, color.g, color.b) - Math.min(color.r, color.g, color.b);
    if (spread < 10 &&
        colorDistance(color, { r: grayValue, g: grayValue, b: grayValue }) < colorDistance(color, cubeColor)) {
        return 232 + grayOffset;
    }
    return cubeIndex;
}
function colorAnsi(color, mode, background) {
    if (color.kind === "indexed")
        return `\x1b[${background ? 48 : 38};5;${color.index}m`;
    const rgb = colorToRgb(color);
    if (mode === "truecolor") {
        return `\x1b[${background ? 48 : 38};2;${Math.round(rgb.r)};${Math.round(rgb.g)};${Math.round(rgb.b)}m`;
    }
    return `\x1b[${background ? 48 : 38};5;${rgbToAnsi256(rgb)}m`;
}
export function foregroundAnsi(color, mode) {
    return colorAnsi(color, mode, false);
}
export function backgroundAnsi(color, mode) {
    return colorAnsi(color, mode, true);
}
export function styleText(text, options, mode) {
    return styleTextWithAnsi(text, options.fg && foregroundAnsi(options.fg, mode), options.bg && backgroundAnsi(options.bg, mode), options);
}
/**
 * Like `styleText()`, but with precomputed color escape sequences, e.g. cached theme colors.
 * Colors in `options` are ignored.
 */
export function styleTextWithAnsi(text, fgAnsi, bgAnsi, options) {
    // Resets are prepended so they close in reverse order of the opening sequences.
    let prefix = "";
    let suffix = "";
    if (fgAnsi) {
        prefix += fgAnsi;
        suffix = "\x1b[39m";
    }
    if (bgAnsi) {
        prefix += bgAnsi;
        suffix = `\x1b[49m${suffix}`;
    }
    if (options.bold)
        prefix += "\x1b[1m";
    if (options.dim)
        prefix += "\x1b[2m";
    if (options.bold || options.dim)
        suffix = `\x1b[22m${suffix}`;
    if (options.italic) {
        prefix += "\x1b[3m";
        suffix = `\x1b[23m${suffix}`;
    }
    if (options.underline) {
        prefix += "\x1b[4m";
        suffix = `\x1b[24m${suffix}`;
    }
    if (options.inverse) {
        prefix += "\x1b[7m";
        suffix = `\x1b[27m${suffix}`;
    }
    if (options.strikethrough) {
        prefix += "\x1b[9m";
        suffix = `\x1b[29m${suffix}`;
    }
    return `${prefix}${text}${suffix}`;
}
