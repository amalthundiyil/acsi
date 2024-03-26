# - Find cares
# Find the native CARES includes and library
#
#  CARES_INCLUDE_DIRS - where to find cares.h, etc.
#  CARES_LIBRARIES    - List of libraries when using cares.
#  CARES_LDFLAGS      - List of library dependencies (libresolv on macOS)
#  CARES_FOUND        - True if cares found.


IF (CARES_INCLUDE_DIRS)
  # Already in cache, be silent
  SET(CARES_FIND_QUIETLY TRUE)
ENDIF (CARES_INCLUDE_DIRS)

FIND_PATH(CARES_INCLUDE_DIR ares.h)

SET(CARES_NAMES cares)
FIND_LIBRARY(CARES_LIBRARY NAMES ${CARES_NAMES} )

# handle the QUIETLY and REQUIRED arguments and set CARES_FOUND to TRUE if
# all listed variables are TRUE
INCLUDE(FindPackageHandleStandardArgs)
FIND_PACKAGE_HANDLE_STANDARD_ARGS(CARES DEFAULT_MSG CARES_LIBRARY CARES_INCLUDE_DIR)

IF(CARES_FOUND)
  SET( CARES_LIBRARIES ${CARES_LIBRARY} )
  if (MACOSX)
    SET( CARES_LDFLAGS -lresolv )
  else ()
    SET( CARES_LDFLAGS )
  endif()

  SET( CARES_INCLUDE_DIRS ${CARES_INCLUDE_DIR} )
ELSE(CARES_FOUND)
  SET( CARES_LIBRARIES )
  SET( CARES_INCLUDE_DIRS )
ENDIF(CARES_FOUND)

MARK_AS_ADVANCED( CARES_LIBRARIES CARES_INCLUDE_DIRS )
