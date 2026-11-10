"""dd_pkg_rpm - wrapper for pkg_rpm adding agent specific defaults."""

load("@rules_pkg//pkg:rpm_pfg.bzl", "pkg_rpm_impl")
load("//bazel/rules/dd_strip:get_debug_symbols.bzl", "get_debug_symbols")
load("//packages/rules:dd_pkg_owned_dirs.bzl", "dd_pkg_owned_dirs")
load("//packages/rules:description.bzl", "rpm_description")

_DEFAULT_DEFINES = {
    "__provides_exclude": ".*",
    "__requires_exclude": ".*",
    # Skip rpmbuild's post-install processing (brp-strip, brp-compress, ...),
    # so packaged files are left untouched. brp-strip would also corrupt eBPF
    # objects and fail on read-only files coming from TreeArtifacts.
    "__spec_install_post": "true",
    "_build_id_links": "none",
}

# rpm only removes directories listed in the package on uninstall. Declare the
# implicit parent directories of srcs so they don't get left behind.
def _rpm_package(name, srcs, defines = None, **kwargs):
    owned_dirs = "%s_owned_dirs_" % name
    dd_pkg_owned_dirs(
        name = owned_dirs,
        srcs = srcs,
        tags = ["manual"],
        visibility = ["//visibility:private"],
    )

    merged_defines = dict(_DEFAULT_DEFINES)
    merged_defines.update(defines or {})
    pkg_rpm_impl(
        name = name,
        srcs = srcs + [":" + owned_dirs],
        binary_payload_compression = select({
            "//:is_release": "w9T8.xzdio",
            "//conditions:default": "w5T8.xzdio",
        }),
        defines = merged_defines,
        **kwargs
    )

def _dd_pkg_rpm_impl(
        name,
        visibility,
        debug_package,
        defines,
        description,
        epoch,
        package_file_name,
        package_name,
        post_scriptlet,
        post_scriptlet_file,
        posttrans_scriptlet,
        posttrans_scriptlet_file,
        postun_scriptlet,
        postun_scriptlet_file,
        pre_scriptlet,
        pre_scriptlet_file,
        preun_scriptlet,
        preun_scriptlet_file,
        provides,
        release,
        requires,
        requires_contextual,
        srcs,
        version,
        **kwargs):
    common = dict(
        defines = defines,
        description = rpm_description(description),
        epoch = epoch,
        release = release,
        version = version,
        visibility = visibility,
    )

    _rpm_package(
        name = name,
        srcs = srcs,
        package_file_name = package_file_name,
        package_name = package_name,
        post_scriptlet = post_scriptlet,
        post_scriptlet_file = post_scriptlet_file,
        posttrans_scriptlet = posttrans_scriptlet,
        posttrans_scriptlet_file = posttrans_scriptlet_file,
        postun_scriptlet = postun_scriptlet,
        postun_scriptlet_file = postun_scriptlet_file,
        pre_scriptlet = pre_scriptlet,
        pre_scriptlet_file = pre_scriptlet_file,
        preun_scriptlet = preun_scriptlet,
        preun_scriptlet_file = preun_scriptlet_file,
        provides = provides,
        requires = requires,
        requires_contextual = requires_contextual,
        **(common | kwargs)
    )

    if not debug_package:
        return

    if not package_name or not package_file_name.startswith(package_name):
        fail("debug_package requires package_file_name to start with package_name, got: %s and %s" % (package_file_name, package_name))

    debug_symbols = "%s_debug_symbols" % name
    get_debug_symbols(
        name = debug_symbols,
        srcs = srcs,
        prefix = "{install_dir}/.debug",
        tags = ["manual"],
        visibility = ["//visibility:private"],
    )

    full_version = "%s-%s" % (version, release)
    if epoch:
        full_version = "%s:%s" % (epoch, full_version)

    _rpm_package(
        name = "%s_dbg" % name,
        srcs = [":" + debug_symbols],
        package_file_name = package_name + "-dbg" + package_file_name.removeprefix(package_name),
        package_name = package_name + "-dbg",
        requires = ["%s = %s" % (package_name, full_version)],
        **(common | kwargs)
    )

dd_pkg_rpm = macro(
    doc = """Build an RPM package.

With debug_package, also build <name>_dbg: a <package_name>-dbg package holding
the debug symbols of the binaries installed by srcs.
""",
    inherit_attrs = pkg_rpm_impl,
    implementation = _dd_pkg_rpm_impl,
    attrs = {
        "binary_payload_compression": None,
        "debug_package": attr.bool(
            doc = "Also build a <name>_dbg debug symbols package.",
            default = False,
            configurable = False,
        ),
        "defines": attr.string_dict(configurable = False),
        "description": attr.string(configurable = False),
        "epoch": attr.string(configurable = False),
        "package_file_name": attr.string(configurable = False),
        "package_name": attr.string(configurable = False),
        "private_stamp_detect": None,
        "release": attr.string(configurable = False),
        "stamp": None,
        "version": attr.string(configurable = False),
    },
)
