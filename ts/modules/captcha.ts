import { _get, _post, formatApiFailure } from "./common.js";

declare var window: GlobalWindow;

export class Captcha {
    isPWR = false;
    enabled = true;
    verified = false;
    captchaID = "";
    input = document.getElementById("captcha-input") as HTMLInputElement;
    checkbox = document.getElementById("captcha-success") as HTMLSpanElement;
    previous = "";
    reCAPTCHA = false;
    code = "";

    get value(): string {
        return this.input.value;
    }

    hasChanged = (): boolean => {
        return this.value != this.previous;
    };

    baseValidatorWrapper = (_baseValidator: (oncomplete: (valid: boolean) => void, captchaValid: boolean) => void) => {
        return (oncomplete: (valid: boolean) => void): void => {
            if (this.enabled && !this.reCAPTCHA && this.hasChanged()) {
                this.previous = this.value;
                this.verify(() => {
                    _baseValidator(oncomplete, this.verified);
                });
            } else {
                _baseValidator(oncomplete, this.verified);
            }
        };
    };

    verify = (callback: () => void) =>
        _post(
            "/captcha/verify/" +
                this.code +
                "/" +
                this.captchaID +
                "/" +
                this.input.value +
                (this.isPWR ? "?pwr=true" : ""),
            null,
            (req: XMLHttpRequest) => {
                if (req.readyState == 4) {
                    if (req.status == 204) {
                        this.checkbox.innerHTML = `<i class="ri-check-line"></i>`;
                        this.checkbox.classList.add("~positive");
                        this.checkbox.classList.remove("~critical");
                        this.verified = true;
                    } else {
                        this.checkbox.innerHTML = `<i class="ri-close-line"></i>`;
                        this.checkbox.classList.add("~critical");
                        this.checkbox.classList.remove("~positive");
                        this.verified = false;
                        if (req.status >= 500) {
                            window.notifications.customError(
                                "captchaVerifyError",
                                formatApiFailure(req, window.lang.notif("errorCaptcha")),
                            );
                        }
                    }
                    callback();
                }
            },
            true,
        );

    generate = () =>
        _get("/captcha/gen/" + this.code + (this.isPWR ? "?pwr=true" : ""), null, (req: XMLHttpRequest) => {
            if (req.readyState == 4) {
                if (req.status == 200) {
                    this.captchaID = req.response["id"];
                    // The ID changes on every generation, so it doubles as the image cache-buster.
                    document.getElementById("captcha-img").innerHTML = `
                <img class="w-full" src="${window.pages ? window.pages.Base : ""}/captcha/img/${this.code}/${this.captchaID}${this.isPWR ? "?pwr=true" : ""}"></img>
                `;
                    this.input.value = "";
                    this.previous = "";
                    this.verified = false;
                } else if (req.status !== 401 && req.status !== 403 && req.status !== 429) {
                    window.notifications.customError(
                        "captchaGenError",
                        formatApiFailure(req, window.lang.notif("errorUnknown")),
                    );
                }
            }
        });

    constructor(code: string, enabled: boolean, reCAPTCHA: boolean, isPWR: boolean) {
        this.code = code;
        this.enabled = enabled;
        this.reCAPTCHA = reCAPTCHA;
        this.isPWR = isPWR;
    }
}

export interface GreCAPTCHA {
    render: (
        container: HTMLDivElement,
        parameters: {
            sitekey?: string;
            theme?: string;
            size?: string;
            tabindex?: number;
            callback?: () => void;
            "expired-callback"?: () => void;
            "error-callback"?: () => void;
        },
    ) => void;
    getResponse: (opt_widget_id?: HTMLDivElement) => string;
}
