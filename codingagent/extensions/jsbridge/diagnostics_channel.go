package jsbridge

import "github.com/grafana/sobek"

func newDiagnosticsChannelModule(runtime *sobek.Runtime) *sobek.Object {
	value, err := runtime.RunString(diagnosticsChannelSource)
	if err != nil {
		panic(runtime.NewTypeError("install node:diagnostics_channel: %s", err))
	}
	return value.ToObject(runtime)
}

const diagnosticsChannelSource = `(function () {
	"use strict";

	const channels = new Map();
	const traceEvents = ["start", "end", "asyncStart", "asyncEnd", "error"];

	function validateFunction(value, name) {
		if (typeof value !== "function") {
			throw new TypeError("The \"" + name + "\" argument must be of type function");
		}
	}

	function deferThrow(error) {
		if (typeof process.nextTick === "function") {
			process.nextTick(function () { throw error; });
		} else {
			setTimeout(function () { throw error; }, 0);
		}
	}

	function wrapStoreRun(store, data, next, transform) {
		return function () {
			let context;
			try {
				context = transform === undefined ? data : transform(data);
			} catch (error) {
				deferThrow(error);
				return next();
			}
			return store.run(context, next);
		};
	}

	class Channel {
		constructor(name) {
			this.name = name;
			this._subscribers = [];
			this._stores = new Map();
			channels.set(name, this);
		}

		get hasSubscribers() {
			return this._subscribers.length !== 0 || this._stores.size !== 0;
		}

		subscribe(subscription) {
			validateFunction(subscription, "subscription");
			this._subscribers = this._subscribers.slice();
			this._subscribers.push(subscription);
		}

		unsubscribe(subscription) {
			const index = this._subscribers.indexOf(subscription);
			if (index === -1) return false;
			this._subscribers = this._subscribers.slice(0, index).concat(this._subscribers.slice(index + 1));
			return true;
		}

		bindStore(store, transform) {
			this._stores.set(store, transform);
		}

		unbindStore(store) {
			return this._stores.delete(store);
		}

		publish(data) {
			const subscribers = this._subscribers;
			for (let index = 0; index < subscribers.length; index++) {
				try {
					subscribers[index](data, this.name);
				} catch (error) {
					deferThrow(error);
				}
			}
		}

		runStores(data, fn, thisArg, ...args) {
			let run = () => {
				this.publish(data);
				return Reflect.apply(fn, thisArg, args);
			};
			for (const [store, transform] of this._stores) {
				run = wrapStoreRun(store, data, run, transform);
			}
			return run();
		}
	}

	function channel(name) {
		const existing = channels.get(name);
		if (existing !== undefined) return existing;
		if (typeof name !== "string" && typeof name !== "symbol") {
			throw new TypeError("The channel argument must be of type string or symbol");
		}
		return new Channel(name);
	}

	function subscribe(name, subscription) {
		return channel(name).subscribe(subscription);
	}

	function unsubscribe(name, subscription) {
		return channel(name).unsubscribe(subscription);
	}

	function hasSubscribers(name) {
		const existing = channels.get(name);
		return existing === undefined ? false : existing.hasSubscribers;
	}

	function tracingChannelFrom(nameOrChannels, name) {
		if (typeof nameOrChannels === "string") {
			return channel("tracing:" + nameOrChannels + ":" + name);
		}
		if (typeof nameOrChannels === "object" && nameOrChannels !== null) {
			const value = nameOrChannels[name];
			if (!(value instanceof Channel)) {
				throw new TypeError("nameOrChannels." + name + " must be a Channel");
			}
			return value;
		}
		throw new TypeError("nameOrChannels must be a string or channel object");
	}

	class TracingChannel {
		constructor(nameOrChannels) {
			for (const name of traceEvents) {
				Object.defineProperty(this, name, {
					configurable: false,
					enumerable: false,
					writable: false,
					value: tracingChannelFrom(nameOrChannels, name),
				});
			}
		}

		get hasSubscribers() {
			return this.start.hasSubscribers || this.end.hasSubscribers ||
				this.asyncStart.hasSubscribers || this.asyncEnd.hasSubscribers ||
				this.error.hasSubscribers;
		}

		subscribe(handlers) {
			for (const name of traceEvents) {
				if (handlers[name]) this[name].subscribe(handlers[name]);
			}
		}

		unsubscribe(handlers) {
			let done = true;
			for (const name of traceEvents) {
				if (handlers[name] && !this[name].unsubscribe(handlers[name])) done = false;
			}
			return done;
		}

		traceSync(fn, context = {}, thisArg, ...args) {
			if (!this.hasSubscribers) return Reflect.apply(fn, thisArg, args);
			return this.start.runStores(context, () => {
				try {
					const result = Reflect.apply(fn, thisArg, args);
					context.result = result;
					return result;
				} catch (error) {
					context.error = error;
					this.error.publish(context);
					throw error;
				} finally {
					this.end.publish(context);
				}
			});
		}

		tracePromise(fn, context = {}, thisArg, ...args) {
			if (!this.hasSubscribers) return Reflect.apply(fn, thisArg, args);
			return this.start.runStores(context, () => {
				try {
					let promise = Reflect.apply(fn, thisArg, args);
					if (!(promise instanceof Promise)) promise = Promise.resolve(promise);
					return promise.then(
						(result) => {
							context.result = result;
							this.asyncStart.publish(context);
							this.asyncEnd.publish(context);
							return result;
						},
						(error) => {
							context.error = error;
							this.error.publish(context);
							this.asyncStart.publish(context);
							this.asyncEnd.publish(context);
							return Promise.reject(error);
						},
					);
				} catch (error) {
					context.error = error;
					this.error.publish(context);
					throw error;
				} finally {
					this.end.publish(context);
				}
			});
		}

		traceCallback(fn, position = -1, context = {}, thisArg, ...args) {
			if (!this.hasSubscribers) return Reflect.apply(fn, thisArg, args);
			const callback = args.at(position);
			validateFunction(callback, "callback");
			const tracing = this;
			function wrappedCallback(error, result) {
				const callbackThis = this;
				const callbackArguments = arguments;
				if (error) {
					context.error = error;
					tracing.error.publish(context);
				} else {
					context.result = result;
				}
				return tracing.asyncStart.runStores(context, () => {
					try {
						return Reflect.apply(callback, callbackThis, callbackArguments);
					} finally {
						tracing.asyncEnd.publish(context);
					}
				});
			}
			args.splice(position, 1, wrappedCallback);
			return this.start.runStores(context, () => {
				try {
					return Reflect.apply(fn, thisArg, args);
				} catch (error) {
					context.error = error;
					this.error.publish(context);
					throw error;
				} finally {
					this.end.publish(context);
				}
			});
		}
	}

	function tracingChannel(nameOrChannels) {
		return new TracingChannel(nameOrChannels);
	}

	return { channel, hasSubscribers, subscribe, tracingChannel, unsubscribe, Channel };
})()`
