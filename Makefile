.PHONY: help setup bench report clean go-dev rust-dev

help:
	@$(MAKE) -C ep07-go-vs-rust-benchmark help

setup:
	@$(MAKE) -C ep07-go-vs-rust-benchmark setup

bench:
	@$(MAKE) -C ep07-go-vs-rust-benchmark bench

report:
	@$(MAKE) -C ep07-go-vs-rust-benchmark report

clean:
	@$(MAKE) -C ep07-go-vs-rust-benchmark clean

go-dev:
	@$(MAKE) -C ep07-go-vs-rust-benchmark go-dev

rust-dev:
	@$(MAKE) -C ep07-go-vs-rust-benchmark rust-dev

